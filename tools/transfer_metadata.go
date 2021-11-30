// transfer_metadata copies ZIP file metadata from one ZIP to another.
//
// When comparing ZIP file contents originating from difference build processes,
// much of the metadata like file modes or order of apprearance have no
// relevance. This utility removes these differences by applying the metadata of
// the source archive to that of the destination.
package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"os"
)

func main() {
	if len(os.Args) != 3 {
		log.Fatal(fmt.Sprintf("Usage: %s <source> <dest>", os.Args[0]))
	}
	sourcePath, destPath := os.Args[1], os.Args[2]
	source, err := ioutil.ReadFile(sourcePath)
	if err != nil {
		log.Fatal(err)
	}
	sourceZip, err := zip.NewReader(bytes.NewReader(source), int64(len(source)))
	if err != nil {
		log.Fatal(err)
	}
	dest, err := ioutil.ReadFile(destPath)
	if err != nil {
		log.Fatal(err)
	}
	destZip, err := zip.NewReader(bytes.NewReader(dest), int64(len(dest)))
	if err != nil {
		log.Fatal(err)
	}
	transferMetadata(sourceZip, destZip)
	transferFileOrder(sourceZip, destZip)
	f, err := os.Create(destPath)
	if err != nil {
		log.Fatal(err)
	}
	w := zip.NewWriter(f)
	defer w.Close()
	for _, f := range destZip.File {
		err = w.Copy(f)
		if err != nil {
			log.Fatal(err)
		}
	}
}

func transferMetadata(source, dest *zip.Reader) {
	sourceByName := make(map[string]*zip.FileHeader, len(source.File))
	for _, f := range source.File {
		sourceByName[f.Name] = &f.FileHeader
	}
	for _, f := range dest.File {
		if sourceByName[f.Name] != nil {
			f.Modified = sourceByName[f.Name].Modified
			f.ModifiedTime = sourceByName[f.Name].ModifiedTime
			f.ModifiedDate = sourceByName[f.Name].ModifiedDate
			f.ExternalAttrs = sourceByName[f.Name].ExternalAttrs
		}
	}
}

func transferFileOrder(source, dest *zip.Reader) {
	var order []string
	for _, f := range source.File {
		order = append(order, f.Name)
	}
	destByName := make(map[string]*zip.File, len(dest.File))
	for _, f := range dest.File {
		destByName[f.Name] = f
	}
	var reordered []*zip.File
	for _, f := range source.File {
		if destByName[f.Name] == nil {
			continue
		}
		reordered = append(reordered, destByName[f.Name])
		delete(destByName, f.Name)
	}
	for _, f := range destByName {
		reordered = append(reordered, f)
	}
	dest.File = reordered
}
