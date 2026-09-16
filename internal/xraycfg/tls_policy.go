package xraycfg

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ApplySecurityPolicy enforces the local ALLOW_INSECURE choice on a finished
// config (A01). A link is built with the subscription's insecure flag behind
// that opt-in, but a panel profile or a preserved outbound runs close to
// verbatim, and its own tlsSettings.allowInsecure used to reach the core past
// ALLOW_INSECURE=false. Applied once to whatever a builder produced, the policy
// holds whichever path the config took.
//
// Without the opt-in the flag is removed from every outbound — each link of a
// dialerProxy chain is an outbound of its own. With it, a flag the config
// already carries stays and none is added. Only
// outbounds[*].streamSettings.tlsSettings is read: a same-named field anywhere
// else belongs to something else. A value that is not a JSON boolean is refused
// rather than guessed at. The input is never modified, and when there is
// nothing to remove it comes back as is.
//
// The four names on that path are matched whatever their case (F05). The core
// decodes JSON into Go structs, which match field names case-insensitively, so
// "TlsSettings" and "AllowInsecure" reached it exactly as the lowercase
// spellings do while an exact-key policy walked straight past them. An object
// that spells one of those names more than once is refused instead: two
// spellings are two answers to the same question, the order a Go map hands them
// back is not one of them, and the core's own structs may merge nested objects.
func ApplySecurityPolicy(raw json.RawMessage, allowInsecure bool) (json.RawMessage, error) {
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	obKey, err := soleKey(raw, "outbounds")
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if obKey == "" || len(cfg[obKey]) == 0 {
		return raw, nil
	}
	var sources []json.RawMessage
	if err := json.Unmarshal(cfg[obKey], &sources); err != nil {
		return nil, fmt.Errorf("outbounds: %w", err)
	}

	changed := false
	outbounds := make([]json.RawMessage, len(sources))
	for i, src := range sources {
		cleaned, cleared, err := clearInsecure(src, allowInsecure)
		if err != nil {
			return nil, fmt.Errorf("outbound %s: %w", outboundName(src, i), err)
		}
		outbounds[i] = cleaned
		changed = changed || cleared
	}
	if !changed {
		return raw, nil
	}

	// Written back under the canonical name: the object is being rewritten
	// anyway, and one spelling in the output is one thing to reason about.
	delete(cfg, obKey)
	if cfg["outbounds"], err = json.Marshal(outbounds); err != nil {
		return nil, fmt.Errorf("outbounds: %w", err)
	}
	return json.Marshal(cfg)
}

// clearInsecure applies the policy to one outbound, returning it as it should
// go to the core and whether the flag was removed.
func clearInsecure(raw json.RawMessage, allowInsecure bool) (json.RawMessage, bool, error) {
	streamKey, err := soleKey(raw, "streamSettings")
	if err != nil {
		return nil, false, err
	}
	if streamKey == "" {
		return raw, false, nil
	}
	var ob map[string]json.RawMessage
	if err := json.Unmarshal(raw, &ob); err != nil {
		return nil, false, err
	}

	tlsKey, err := soleKey(ob[streamKey], "tlsSettings")
	if err != nil {
		return nil, false, fmt.Errorf("streamSettings: %w", err)
	}
	if tlsKey == "" {
		// Nothing of ours here — but a value that is not an object at all is a
		// config the core will not read the way this policy just did.
		if err := checkObject(ob[streamKey], "streamSettings"); err != nil {
			return nil, false, err
		}
		return raw, false, nil
	}
	var stream map[string]json.RawMessage
	if err := json.Unmarshal(ob[streamKey], &stream); err != nil {
		return nil, false, fmt.Errorf("streamSettings: %w", err)
	}

	flagKey, err := soleKey(stream[tlsKey], "allowInsecure")
	if err != nil {
		return nil, false, fmt.Errorf("tlsSettings: %w", err)
	}
	if flagKey == "" {
		if err := checkObject(stream[tlsKey], "tlsSettings"); err != nil {
			return nil, false, err
		}
		return raw, false, nil
	}
	var tls map[string]json.RawMessage
	if err := json.Unmarshal(stream[tlsKey], &tls); err != nil {
		return nil, false, fmt.Errorf("tlsSettings: %w", err)
	}

	var flag *bool
	if err := json.Unmarshal(tls[flagKey], &flag); err != nil || flag == nil {
		return nil, false, errors.New("tlsSettings.allowInsecure is not true or false")
	}
	if allowInsecure {
		// The opt-in adds nothing and renames nothing: the config goes on as it
		// came, which is also what keeps an untouched input byte-identical.
		return raw, false, nil
	}

	delete(tls, flagKey)
	delete(stream, tlsKey)
	delete(ob, streamKey)
	if stream["tlsSettings"], err = json.Marshal(tls); err != nil {
		return nil, false, err
	}
	if ob["streamSettings"], err = json.Marshal(stream); err != nil {
		return nil, false, err
	}
	out, err := json.Marshal(ob)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}

// soleKey names the one key of a JSON object that stands for field, whatever
// its case. An object naming it more than once — two spellings, or the same
// spelling twice — is refused: a Go map keeps one of them and loses the order,
// so there is no honest way to say which value the core would end up reading.
// An absent key, and a null where an object could be, answer with no key and no
// error; the caller decides whether that is a problem.
func soleKey(obj json.RawMessage, field string) (string, error) {
	if isJSONNull(obj) {
		return "", nil
	}
	dec := json.NewDecoder(bytes.NewReader(obj))
	tok, err := dec.Token()
	if err != nil {
		return "", err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return "", nil // not an object: nothing of ours is in there
	}

	var found string
	seen := 0
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		key, ok := tok.(string)
		if !ok {
			return "", fmt.Errorf("%s: unexpected JSON key", field)
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return "", err
		}
		if strings.EqualFold(key, field) {
			seen++
			found = key
		}
	}
	if seen > 1 {
		return "", fmt.Errorf("%s указан %d раза — какое из значений возьмёт ядро, не определено; оставьте одно", field, seen)
	}
	return found, nil
}

// checkObject refuses a value that stands where an object has to stand. soleKey
// stays quiet about it so that a missing key and a null read the same; here the
// path was expected to continue, and a string or an array in its place is a
// config this policy cannot claim to have checked.
func checkObject(v json.RawMessage, what string) error {
	if len(v) == 0 || isJSONNull(v) {
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(v, &obj); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

func isJSONNull(v json.RawMessage) bool {
	return len(v) == 0 || bytes.Equal(bytes.TrimSpace(v), []byte("null"))
}

// outboundName labels an outbound for an error: its tag when it has one.
func outboundName(raw json.RawMessage, i int) string {
	var ob struct {
		Tag string `json:"tag"`
	}
	if json.Unmarshal(raw, &ob) == nil && ob.Tag != "" {
		return ob.Tag
	}
	return fmt.Sprintf("#%d", i)
}
