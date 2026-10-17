package sshd

import (
	"github.com/Boulea7/shellpier/internal/terminals"
	"golang.org/x/crypto/ssh"
)

const TerminalsRequest = "terminals@shellpier"

// HandleTerminalsRequest runs only after authentication and access-mode checks.
func HandleTerminalsRequest(manager *terminals.Manager, request *ssh.Request) bool {
	if request.Type != TerminalsRequest {
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
