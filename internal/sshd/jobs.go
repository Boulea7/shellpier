package sshd

import (
	"github.com/Boulea7/shellpier/internal/jobs"
	"golang.org/x/crypto/ssh"
)

const JobsRequest = "jobs@shellpier"

// HandleJobsRequest is called only after SSH authentication and host opt-in.
func HandleJobsRequest(manager *jobs.Manager, request *ssh.Request) bool {
	if request.Type != JobsRequest {
		return false
	}
	if manager == nil {
		request.Reply(false, nil)
		return true
	}
	response, err := manager.Handle(request.Payload)
	if err != nil {
		request.Reply(false, nil)
		return true
	}
	request.Reply(true, response)
	return true
}
