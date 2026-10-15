package cli

import (
	"encoding/json"
	"strings"

	"github.com/Boulea7/shellpier/internal/access"
)

func mcpCredentials(args []string) []string {
	result := []string{}
	for _, arg := range args {
		if strings.HasPrefix(arg, "@") || strings.HasPrefix(arg, "target:") {
			if ticket, err := access.Read(arg); err == nil {
				result = append(result, ticket.PrivateKey, ticket.HostPrivateKey, ticket.RelayToken)
			}
		}
		if strings.HasPrefix(arg, "--state=") {
			path := strings.TrimPrefix(arg, "--state=")
			if state, err := readLocalState(path); err == nil {
				result = append(result, state.Secret)
			}
			if state, err := terminalReadState(path, false); err == nil {
				result = append(result, state.Secret)
			}
		}
	}
	return result
}

func mcpRedact(data []byte, secrets []string) []byte {
	text := string(data)
	for _, secret := range secrets {
		if len(secret) < 16 {
			continue
		}
		text = mcpRedactValue(text, secret)
		encoded, _ := json.Marshal(secret)
		if len(encoded) > 2 {
			text = mcpRedactValue(text, string(encoded[1:len(encoded)-1]))
		}
	}
	var value any
	if json.Unmarshal([]byte(text), &value) == nil {
		mcpRedactFields(value)
		if encoded, err := json.Marshal(value); err == nil {
			return encoded
		}
	}
	return []byte(text)
}

func mcpRedactFields(value any) {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			switch strings.ToLower(key) {
			case "secret", "private_key", "host_private_key", "relay_token", "host_token", "client_token", "creation_key", "authorization", "ticket":
				v[key] = "[redacted]"
			default:
				mcpRedactFields(item)
			}
		}
	case []any:
		for _, item := range v {
			mcpRedactFields(item)
		}
	}
}

func mcpRedactValue(text, secret string) string {
	text = strings.ReplaceAll(text, secret, "[redacted]")
	if len(secret) >= 16 {
		if index := strings.Index(text, secret[:16]); index >= 0 && strings.HasPrefix(secret, text[index:]) {
			text = text[:index] + "[redacted]"
		}
	}
	return text
}
