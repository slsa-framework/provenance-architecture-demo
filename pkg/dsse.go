package main

import (
	"context"
	"encoding/base64"
	"fmt"

	kms "cloud.google.com/go/kms/apiv1"
	kmspb "google.golang.org/genproto/googleapis/cloud/kms/v1"
)

const (
	inTotoPayloadType = "application/vnd.in-toto+json"
)

type DSSE struct {
	PayloadType string      `json:"payloadType"`
	Payload     string      `json:"payload"`
	Signatures  []Signature `json:"signatures"`
}

type Signature struct {
	KeyID string `json:"keyid"`
	Sig   string `json:"sig"`
}

func NewDSSE(payload []byte) (DSSE, error) {
	encodedPayload := base64.StdEncoding.EncodeToString(payload)
	encoded := fmt.Sprintf("DSSEv1 %d %s %d %s", len(inTotoPayloadType), inTotoPayloadType, len(encodedPayload), encodedPayload)
	sig, err := kmsSign(*kmsKey, []byte(encoded))
	if err != nil {
		return DSSE{}, err
	}
	return DSSE{
		PayloadType: inTotoPayloadType,
		Payload:     encodedPayload,
		Signatures: []Signature{{
			KeyID: "https://cloudkms.googleapis.com/" + *kmsKey,
			Sig:   base64.StdEncoding.EncodeToString(sig),
		}},
	}, nil
}

func kmsSign(keyName string, payload []byte) ([]byte, error) {
	ctx := context.Background()
	c, err := kms.NewKeyManagementClient(ctx)
	if err != nil {
		return []byte{}, err
	}
	defer c.Close()

	req := &kmspb.AsymmetricSignRequest{
		Name: keyName,
		Data: payload,
	}
	resp, err := c.AsymmetricSign(ctx, req)
	if err != nil {
		return []byte{}, err
	}
	return resp.Signature, nil
}
