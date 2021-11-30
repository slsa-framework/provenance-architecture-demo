package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"time"
)

type PyPiProject struct {
	Info     `json:"info"`
	Releases map[string][]Release `json:"releases"`
}
type Info struct {
	LatestVersion string `json:"version"`
}
type Release struct {
	Digests       `json:"digests"`
	Filename      string    `json:"filename"`
	PackageType   string    `json:"packagetype"`
	PythonVersion string    `json:"python_version"`
	URL           string    `json:"url"`
	UploadTime    time.Time `json:"upload_time_iso_8601"`
}
type Digests struct {
	MD5    string `json:"md5"`
	SHA256 string `json:"sha256"`
}

func get(url string) []byte {
	resp, err := http.Get(url)
	if err != nil {
		log.Fatal(err)
	}
	bytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}
	return bytes
}

func pypiMetadata(pkg string) PyPiProject {
	bytes := get(fmt.Sprintf("https://pypi.org/pypi/%s/json", pkg))
	project := PyPiProject{}
	if err := json.Unmarshal(bytes, &project); err != nil {
		log.Fatal(err)
	}
	return project
}
