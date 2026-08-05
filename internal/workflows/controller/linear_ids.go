package controller

import (
	"crypto/rand"
	"encoding/base32"
)

func newWorkflowRunID() string {
	return "wfr-" + randomWorkflowToken()
}

func newWorkflowHolder() string {
	return "controller-" + randomWorkflowToken()
}

func newWorkflowTaskID() string {
	return "wft-" + randomWorkflowToken()
}

func randomWorkflowToken() string {
	var value [10]byte
	_, _ = rand.Read(value[:])
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(value[:])
}
