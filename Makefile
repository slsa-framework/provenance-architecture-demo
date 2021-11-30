.PHONY: images all

ifeq ("${GCP_PROJECT}", "")
    $(error "Must set GCP_PROJECT")
endif

all: images

images: transfer_metadata server

server: pkg/*.go build/server.Dockerfile
	docker build -f build/server.Dockerfile -t server .
	docker tag server gcr.io/${GCP_PROJECT}/server
	docker push gcr.io/${GCP_PROJECT}/server

transfer_metadata: tools/transfer_metadata.go build/transfer_metadata.Dockerfile
	docker build -f build/transfer_metadata.Dockerfile -t transfer_metadata .
	docker tag transfer_metadata gcr.io/${GCP_PROJECT}/transfer_metadata
	docker push gcr.io/${GCP_PROJECT}/transfer_metadata

