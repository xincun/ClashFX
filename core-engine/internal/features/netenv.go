package features

import "strings"

type ActiveNetworkSignature struct {
	ProfileNames []string `json:"profile_names,omitempty"`
	Interfaces   []string `json:"interfaces,omitempty"`
	DNSSuffixes  []string `json:"dns_suffixes,omitempty"`
	Gateways     []string `json:"gateways,omitempty"`
}

// MatchSuspendNetwork checks whether any active network signature token (Windows
// Network Profile Name, DNS suffix, gateway IP, or interface alias) matches one
// of the user's configured auto-suspend rules (case-insensitive).
func MatchSuspendNetwork(sig ActiveNetworkSignature, rules []string) (matchedRule string, shouldSuspend bool) {
	if len(rules) == 0 {
		return "", false
	}
	tokens := make([]string, 0, len(sig.ProfileNames)+len(sig.Interfaces)+len(sig.DNSSuffixes)+len(sig.Gateways))
	for _, list := range [][]string{sig.ProfileNames, sig.Interfaces, sig.DNSSuffixes, sig.Gateways} {
		for _, item := range list {
			t := strings.TrimSpace(item)
			if t != "" {
				tokens = append(tokens, t)
			}
		}
	}
	for _, rawRule := range rules {
		rule := strings.TrimSpace(rawRule)
		if rule == "" {
			continue
		}
		for _, tok := range tokens {
			if strings.EqualFold(tok, rule) {
				return rule, true
			}
		}
	}
	return "", false
}
