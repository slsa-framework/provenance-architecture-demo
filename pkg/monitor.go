package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/go-github/v40/github"
	"github.com/in-toto/in-toto-golang/in_toto"
)

type MonitorOptions struct {
	GitHubActions
	Version *string
}

func MonitorBuild(pkg, repo string, opt MonitorOptions) (*in_toto.ProvenanceStatement, error) {
	if !strings.HasPrefix(repo, "github.com/") {
		return nil, errors.New("Non-github repos not yet supported")
	}
	parts := strings.Split(repo, "/")
	owner, repo := parts[1], parts[2]
	project := pypiMetadata(pkg)
	var version string
	if opt.Version == nil || *opt.Version == "" {
		version = project.LatestVersion
	} else {
		version = *opt.Version
	}
	releasedFiles := make(map[string]time.Time, len(project.Releases[version]))
	for _, r := range project.Releases[version] {
		releasedFiles[r.Filename] = r.UploadTime
	}
	c := githubClient(*githubToken)
	ctx := context.Background()
	wfs, _, err := c.Actions.ListWorkflows(ctx, owner, repo, nil)
	if err != nil {
		log.Fatalln(err)
	}
	if wfs.GetTotalCount() == 0 {
		return nil, errors.New("No workflows found")
	}
	var wf github.Workflow
	for _, w := range wfs.Workflows {
		if w.GetName() == opt.Workflow {
			wf = *w
		}
	}
	if wf.ID == nil {
		return nil, errors.New("No workflow match")
	}
	rs, _, err := c.Actions.ListWorkflowRunsByID(ctx, owner, repo, *wf.ID, nil)
	if err != nil {
		log.Fatalln(err)
	}
	for _, r := range rs.WorkflowRuns {
		js, _, err := c.Actions.ListWorkflowJobs(ctx, owner, repo, *r.ID, nil)
		if err != nil {
			log.Fatalln(err)
		}
		var timely bool
		for _, uploaded := range releasedFiles {
			if r.GetCreatedAt().Time.Before(uploaded) && r.GetUpdatedAt().Time.After(uploaded) {
				timely = true
			}
		}
		if !timely {
			continue
		}
		if opt.RequireSucceeded != nil {
			var found, succeeded bool
			for _, j := range js.Jobs {
				if *j.Name == opt.RequireSucceeded.Job {
					if opt.RequireSucceeded.Step == "" {
						succeeded = *j.Conclusion == "success"
						found = true
					}
					for _, s := range j.Steps {
						if *s.Name == opt.RequireSucceeded.Step {
							succeeded = *s.Conclusion == "success"
							found = true
						}
					}
				}
			}
			if !found {
				// TODO: Add a warning?
				continue
			}
			if !succeeded {
				continue
			}
		}
		var subjects []in_toto.Subject
		as, _, err := c.Actions.ListWorkflowRunArtifacts(ctx, owner, repo, *r.ID, nil)
		var expired bool
		for _, a := range as.Artifacts {
			var match *ArtifactSpec
			for _, spec := range opt.Artifacts {
				if spec.Name == a.GetName() {
					match = &spec
				}
			}
			if match == nil {
				continue
			}
			if a.GetExpired() {
				expired = true
				break
			}
			u, err := url.Parse(a.GetArchiveDownloadURL())
			if err != nil {
				return nil, err
			}
			var h http.Client
			resp, err := h.Do(&http.Request{
				URL:    u,
				Header: http.Header{"Authorization": []string{fmt.Sprintf("Bearer %s", *githubToken)}},
			})
			if err != nil {
				return nil, err
			}
			if resp.StatusCode != 200 {
				return nil, errors.New("Bad response code")
			}
			archive, err := io.ReadAll(resp.Body)
			if err != nil {
				return nil, err
			}
			zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
			if err != nil {
				return nil, err
			}
			for _, f := range zr.File {
				var matched bool
				for _, path := range match.Patterns {
					m, err := filepath.Match(path, f.Name)
					if err != nil {
						return nil, err
					}
					matched = matched || m
				}
				if !matched {
					log.Printf("Excluding subject file [artifact=%s file=%s]", a.GetName(), f.Name)
					continue
				}
				var timely bool
				var realUpload time.Time
				for fname, uploaded := range releasedFiles {
					if fname == f.Name {
						timely = r.GetCreatedAt().Time.Before(uploaded) && r.GetUpdatedAt().Time.After(uploaded)
						realUpload = uploaded
						break
					}
				}
				if !timely {
					log.Printf("Excluding subject file [artifact=%s file=%s ran=[from=%s to=%s] uploaded=%s]", a.GetName(), f.Name, r.GetCreatedAt(), r.GetUpdatedAt(), realUpload)
					continue
				}
				h := sha256.New()
				reader, err := f.Open()
				if err != nil {
					return nil, err
				}
				if _, err := io.Copy(h, reader); err != nil {
					log.Fatal(err)
				}
				subjects = append(subjects, in_toto.Subject{
					Name:   f.Name,
					Digest: in_toto.DigestSet{"sha256": hex.EncodeToString(h.Sum(nil))},
				})
			}
		}
		if expired {
			log.Println("Skipping: Expired artifact")
			continue
		}
		if len(subjects) == 0 {
			log.Println("Skipping: No artifacts to sign")
			continue
		}
		sort.Slice(subjects, func(i, j int) bool { return subjects[i].Name < subjects[j].Name })
		stmt := in_toto.ProvenanceStatement{
			in_toto.StatementHeader{
				Type:          "https://in-toto.io/Statement/v0.1",
				PredicateType: "https://slsa.dev/provenance/v0.1",
				Subject:       subjects,
			},
			in_toto.ProvenancePredicate{
				in_toto.ProvenanceBuilder{ID: "https://attestations.github.com/actions-workflow/unknown-runner@v1"},
				in_toto.ProvenanceRecipe{
					Type:              "https://slsa.dev/workflows/GitHubActionsWorkflow",
					DefinedInMaterial: new(int),
					EntryPoint:        wf.GetPath(),
					Arguments:         []string{}, // TODO
					Environment:       []string{},
				},
				&in_toto.ProvenanceMetadata{
					BuildStartedOn:  &r.CreatedAt.Time,
					BuildFinishedOn: &r.UpdatedAt.Time,
					Completeness:    in_toto.ProvenanceComplete{Arguments: false, Environment: false, Materials: false},
					Reproducible:    false,
				},
				[]in_toto.ProvenanceMaterial{
					{
						URI:    fmt.Sprintf("git+%s@%s", r.GetHeadRepository().GetHTMLURL(), r.GetHeadBranch()),
						Digest: in_toto.DigestSet{"sha1": r.GetHeadSHA()},
					},
				},
			},
		}
		return &stmt, nil
	}
	return nil, nil
}
