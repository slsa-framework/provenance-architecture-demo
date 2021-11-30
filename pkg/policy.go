package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/google/go-github/v40/github"
	"gopkg.in/yaml.v2"
)

type Policy struct {
	Repo             string
	BuildMonitor     *BuildMonitor     `yaml:"build_monitor"`
	Rebuilder        *Rebuilder        `yaml:"rebuilder"`
	ProvenanceUpload *ProvenanceUpload `yaml:"provenance_upload"`
	Digest           string
	Scope            string
	Package          string
}
type Rebuilder struct {
	PackageRoot string `yaml:"package_root"`
}
type ProvenanceUpload struct {
	AuthorizedBuilders []string `yaml:"authorized_builders"`
}
type BuildMonitor struct {
	GitHubActions `yaml:"github_actions"`
}
type GitHubActions struct {
	Workflow         string
	Artifacts        []ArtifactSpec
	RequireSucceeded *CompletionSpec `yaml:"require_succeeded"`
}
type ArtifactSpec struct {
	Name     string
	Patterns []string
}
type CompletionSpec struct {
	Job  string
	Step string
}

func fetchPolicy(c *github.Client, scope, pkg, ref string) (*Policy, error) {
	file, _, _, err := c.Repositories.GetContents(
		context.Background(), *policyRepoOwner, *policyRepoName, filepath.Join(*policyRepoDir, scope, pkg, "policy.yaml"), &github.RepositoryContentGetOptions{Ref: ref})
	if err != nil {
		return nil, err
	}
	content, err := file.GetContent()
	if err != nil {
		return nil, err
	}
	var np Policy
	if err := yaml.Unmarshal([]byte(content), &np); err != nil {
		return nil, err
	}
	h := sha256.Sum256([]byte(content))
	np.Digest = hex.EncodeToString(h[:])
	np.Scope = scope
	np.Package = pkg
	return &np, nil
}

func fetchPolicies(ref string) (*[]Policy, error) {
	gitfs := memfs.New()
	storer := memory.NewStorage()
	_, err := git.Clone(storer, gitfs, &git.CloneOptions{
		URL:           fmt.Sprintf("https://github.com/%s/%s.git", *policyRepoOwner, *policyRepoName),
		SingleBranch:  true,
		ReferenceName: plumbing.NewBranchReferenceName(ref),
	})
	if err != nil {
		return nil, err
	}
	dirs := []string{*policyRepoDir}
	var paths []string
	for len(dirs) > 0 {
		dir := dirs[len(dirs)-1]
		dirs = dirs[:len(dirs)-1]
		files, err := gitfs.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			switch {
			case f.IsDir():
				dirs = append(dirs, filepath.Join(dir, f.Name()))
			default:
				if f.Name() == "policy.yaml" {
					paths = append(paths, filepath.Join(dir, f.Name()))
				}
			}
		}
	}
	var policies []Policy
	for _, path := range paths {
		f, err := gitfs.Open(path)
		content, err := ioutil.ReadAll(f)
		if err != nil {
			return nil, err
		}
		var np Policy
		if err := yaml.Unmarshal(content, &np); err != nil {
			return nil, err
		}
		h := sha256.Sum256([]byte(content))
		np.Digest = hex.EncodeToString(h[:])
		parts := strings.Split(path, string(os.PathSeparator))
		np.Scope = parts[0]
		np.Package = parts[1]
		policies = append(policies, np)
	}
	return &policies, nil
}
