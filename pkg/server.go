package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/golang-jwt/jwt"
	"github.com/in-toto/in-toto-golang/in_toto"
)

var (
	project         = flag.String("project", "", "GCP Project ID for storage and build resources")
	githubToken     = flag.String("github_token", "", "Auth token for github API. Must have `public_repo` scope.")
	policyRepoOwner = flag.String("policy_repo_owner", "", "Owner of the github policy repo in github.com/owner/name")
	policyRepoName  = flag.String("policy_repo_name", "", "Name of the github policy repo in github.com/owner/name")
	policyRepoDir   = flag.String("policy_repo_dir", ".", "Relative path of the policy hierarchy within the policy repo")
	kmsKey          = flag.String("kms_key", "", "CryptoKeyVersion Resource name of the provenance signing key")
)

func HandleUpload(rw http.ResponseWriter, req *http.Request) {
	email, _, err := authenticatedUser(req)
	if err != nil {
		log.Println(err)
		http.Error(rw, "Authorization parse failed", 403)
		return
	}
	ctx := context.Background()
	gh := githubClient(*githubToken)
	req.ParseForm()
	scope, pkg, version, provenance := req.Form.Get("scope"), req.Form.Get("pkg"), req.Form.Get("version"), req.Form.Get("provenance")
	policy, err := fetchPolicy(&gh, scope, pkg, "main")
	if err != nil {
		log.Println(err)
		http.Error(rw, "Failed to fetch policy", 500)
		return
	}
	if policy.ProvenanceUpload == nil {
		http.Error(rw, "Policy does not define provenance_upload", 400)
		return
	}
	var match bool
	for _, authorized := range policy.ProvenanceUpload.AuthorizedBuilders {
		match = match || authorized == email
	}
	if !match {
		http.Error(rw, "Builder not authorized", 403)
		return
	}
	stmt := in_toto.ProvenanceStatement{}
	if err := json.Unmarshal([]byte(provenance), &stmt); err != nil {
		http.Error(rw, "Malformed provenance", 400)
		return
	}
	stmtBytes, err := in_toto.EncodeCanonical(stmt)
	if err != nil {
		http.Error(rw, "Failed to canonicalize provenance", 400)
		return
	}
	dsse, err := NewDSSE(stmtBytes)
	if err != nil {
		log.Fatal(err)
	}
	dsseBytes, err := json.Marshal(dsse)
	if err != nil {
		log.Fatalln(err)
	}
	client, err := firestore.NewClient(ctx, *project)
	if err != nil {
		http.Error(rw, "Internal Error", 500)
		return
	}
	// XXX should users be able to overwrite uploaded+signed provenance?
	_, err = client.Collection("attestations").Doc(pkg+"!"+version).Set(ctx, map[string]interface{}{
		"package": pkg,
		"version": version,
		"raw":     string(stmtBytes),
		"dsse":    string(dsseBytes),
	})
	if err != nil {
		http.Error(rw, "Internal Error", 500)
		return
	}
}

func authenticatedUser(r *http.Request) (email string, userID string, err error) {
	assertion := strings.TrimPrefix(r.Header.Get("Authorization"), "bearer ")
	if len(assertion) == 0 {
		return "", "", fmt.Errorf("No auth header found")
	}
	parser := jwt.Parser{}
	tok, _, err := parser.ParseUnverified(assertion, jwt.MapClaims{})
	if err != nil {
		return "", "", err
	}
	claims, ok := tok.Claims.(jwt.MapClaims)
	if !ok {
		return "", "", fmt.Errorf("could not extract claims (%T): %+v", tok.Claims, tok.Claims)
	}
	return claims["email"].(string), claims["sub"].(string), nil
}

func HandleRebuild(rw http.ResponseWriter, req *http.Request) {
	ctx := context.Background()
	gh := githubClient(*githubToken)
	req.ParseForm()
	scope, pkg, version, ref := req.Form.Get("scope"), req.Form.Get("pkg"), req.Form.Get("version"), req.Form.Get("ref")
	if ref == "" {
		ref = "main"
	}
	policy, err := fetchPolicy(&gh, scope, pkg, ref)
	if err != nil {
		log.Println(err)
		http.Error(rw, "Failed to fetch policy", 500)
		return
	}
	if policy.Rebuilder == nil {
		http.Error(rw, "Policy does not define rebuilder", 400)
		return
	}
	client, err := firestore.NewClient(ctx, *project)
	if err != nil {
		http.Error(rw, "Internal Error", 500)
		return
	}
	record := map[string]interface{}{
		"package":          pkg,
		"version":          version,
		"status":           "",
		"message":          "",
		"policy_version":   policy.Digest,
		"executor_version": os.Getenv("K_REVISION"),
		"start_time":       time.Now(),
		"end_time":         time.Now(),
	}
	stmts, err := Rebuild(pkg, policy.Repo, RebuilderOptions{
		Version:     &version,
		PackageRoot: &policy.Rebuilder.PackageRoot,
		Types:       []ReleaseType{wheelAny},
	})
	record["end_time"] = time.Now()
	switch {
	case err != nil && strings.HasPrefix(err.Error(), "Rebuild contained diffs"):
		log.Println(err)
		http.Error(rw, "Rebuild contained diffs", 409)
		record["status"] = "failed"
		record["message"] = err.Error()
	case err != nil:
		log.Println(err)
		http.Error(rw, "Failed to rebuild", 500)
		record["status"] = "error"
		record["message"] = "Failed to rebuild"
	case stmts == nil && len(*stmts) == 0:
		http.Error(rw, "No artifacts to rebuild", 404)
		record["status"] = "failure"
		record["message"] = "No artifacts to rebuild"
	default:
		if len(*stmts) != 1 {
			log.Fatalln("Unexpected returned statements")
		}
		builtVersion := strings.Split(filepath.Base((*stmts)[0].Subject[0].Name), "-")[1]
		switch {
		case version == "":
			record["version"] = builtVersion
		case builtVersion != version:
			log.Fatalln("Requested version differs from actual")
		}
		stmtBytes, err := in_toto.EncodeCanonical((*stmts)[0])
		if err != nil {
			log.Fatalln(err)
		}
		dsse, err := NewDSSE(stmtBytes)
		if err != nil {
			log.Fatalln(err)
		}
		dsseBytes, err := json.Marshal(dsse)
		if err != nil {
			log.Fatalln(err)
		}
		_, err = client.Collection("attestations").Doc(pkg+"!"+record["version"].(string)).Set(ctx, map[string]interface{}{
			"package": pkg,
			"version": record["version"].(string),
			"raw":     string(stmtBytes),
			"dsse":    string(dsseBytes),
		})
		if err != nil {
			http.Error(rw, "Internal Error", 500)
			return
		}
		record["status"] = "success"
	}
	if _, _, err = client.Collection("rebuilds").Add(ctx, record); err != nil {
		log.Println("Failed to write record")
	}
}

func HandleMonitor(rw http.ResponseWriter, req *http.Request) {
	ctx := context.Background()
	gh := githubClient(*githubToken)
	req.ParseForm()
	scope, pkg, version, ref := req.Form.Get("scope"), req.Form.Get("pkg"), req.Form.Get("version"), req.Form.Get("ref")
	if ref == "" {
		ref = "main"
	}
	policy, err := fetchPolicy(&gh, scope, pkg, ref)
	if err != nil {
		log.Println(err)
		http.Error(rw, "Failed to fetch policy", 500)
		return
	}
	if policy.BuildMonitor == nil {
		http.Error(rw, "Policy does not define build_monitor", 400)
		return
	}
	client, err := firestore.NewClient(ctx, *project)
	if err != nil {
		http.Error(rw, "Internal Error", 500)
		return
	}
	record := map[string]interface{}{
		"package":          pkg,
		"version":          version,
		"status":           "",
		"message":          "",
		"policy_version":   policy.Digest,
		"executor_version": os.Getenv("K_REVISION"),
		"start_time":       time.Now(),
		"end_time":         time.Now(),
	}
	stmt, err := MonitorBuild(pkg, policy.Repo, MonitorOptions{policy.BuildMonitor.GitHubActions, &version})
	record["end_time"] = time.Now()
	switch {
	case err != nil:
		log.Println(err)
		http.Error(rw, "Failed to monitor build", 500)
		record["status"] = "error"
		record["message"] = "Failed to monitor build"
	case stmt == nil:
		http.Error(rw, "No build found", 404)
		record["status"] = "failure"
		record["message"] = "No build found"
	default:
		var builtVersion string
		for _, subj := range stmt.Subject {
			if !strings.HasSuffix(subj.Name, ".whl") {
				continue
			}
			builtVersion = strings.Split(filepath.Base(subj.Name), "-")[1]
			break
		}
		switch {
		case version == "":
			record["version"] = builtVersion
		case builtVersion != version:
			log.Fatalln("Requested version differs from actual")
		}
		stmtBytes, err := in_toto.EncodeCanonical(stmt)
		if err != nil {
			log.Fatal(err)
		}
		dsse, err := NewDSSE(stmtBytes)
		if err != nil {
			log.Fatal(err)
		}
		dsseBytes, err := json.Marshal(dsse)
		if err != nil {
			log.Fatalln(err)
		}
		_, err = client.Collection("attestations").Doc(pkg+"!"+record["version"].(string)).Set(ctx, map[string]interface{}{
			"package": pkg,
			"version": record["version"].(string),
			"raw":     string(stmtBytes),
			"dsse":    string(dsseBytes),
		})
		if err != nil {
			http.Error(rw, "Internal Error", 500)
			return
		}
	}
	if _, _, err = client.Collection("monitors").Add(ctx, record); err != nil {
		log.Println("Failed to write record")
	}
}

func HandleGet(rw http.ResponseWriter, req *http.Request) {
	ctx := context.Background()
	req.ParseForm()
	// FIXME encode scope in docref
	_, pkg, version := req.Form.Get("scope"), req.Form.Get("pkg"), req.Form.Get("version")
	client, err := firestore.NewClient(ctx, *project)
	if err != nil {
		http.Error(rw, "Internal Error", 500)
		return
	}
	snapshot, err := client.Collection("attestations").Doc(pkg + "!" + version).Get(ctx)
	if err != nil {
		http.Error(rw, "Not Found", 404)
		return
	}
	prov := Provenance{
		Package: snapshot.Data()["package"].(string),
		Version: snapshot.Data()["version"].(string),
		Raw:     snapshot.Data()["raw"].(string),
		DSSE:    snapshot.Data()["dsse"].(string),
	}
	stmt := in_toto.ProvenanceStatement{}
	if err := json.Unmarshal([]byte(prov.Raw), &stmt); err != nil {
		http.Error(rw, "Internal Error", 500)
		return
	}
	_, err = in_toto.EncodeCanonical(stmt)
	if err != nil {
		http.Error(rw, "Internal Error", 500)
		return
	}
	dsse := DSSE{}
	if err := json.Unmarshal([]byte(prov.DSSE), &dsse); err != nil {
		http.Error(rw, "Internal Error", 500)
		return
	}
	ret, err := json.Marshal(prov)
	if err != nil {
		http.Error(rw, "Internal Error", 500)
		return
	}
	rw.Write(ret)
}

type Provenance struct {
	Package string `json:"package"`
	Version string `json:"version"`
	Raw     string `json:"raw"`
	DSSE    string `json:"dsse"`
}

func main() {
	flag.Parse()
	http.HandleFunc("/rebuild", HandleRebuild)
	http.HandleFunc("/monitor", HandleMonitor)
	http.HandleFunc("/upload", HandleUpload)
	http.HandleFunc("/get", HandleGet)
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatalln(err)
	}
}
