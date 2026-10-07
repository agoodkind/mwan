package networkjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Canonicalize sorts object members by decoded name and emits compact JSON.
// Array order, string values, and number literals remain unchanged.
// Canonicalize rejects invalid JSON, invalid UTF-8, trailing data, and lone
// UTF-16 surrogate escapes. Canonicalize rejects objects containing two decoded
// member names equal under [strings.EqualFold].
func Canonicalize(data []byte) ([]byte, error) {
	// The decoder would replace invalid UTF-8 with U+FFFD in string values.
	if !utf8.Valid(data) {
		err := errors.New("canonicalize: document is not valid UTF-8")
		slog.Error("networkjson: canonicalize found invalid UTF-8", "err", err)
		return nil, err
	}
	// json.Decoder.Token replaces lone UTF-16 surrogate escapes with U+FFFD.
	if offset, found := loneSurrogateOffset(data); found {
		err := fmt.Errorf("canonicalize: lone UTF-16 surrogate escape at byte offset %d", offset)
		slog.Error("networkjson: canonicalize found a lone surrogate escape", "err", err)
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var output bytes.Buffer
	if err := writeCanonicalValue(decoder, &output); err != nil {
		slog.Error("networkjson: canonicalize failed", "err", err)
		return nil, fmt.Errorf("canonicalize: %w", err)
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) {
		slog.Error("networkjson: canonicalize found trailing data", "token", token, "err", err)
		return nil, errors.New("canonicalize: trailing data after the top-level value")
	}
	return output.Bytes(), nil
}

// The scan does not track string boundaries because valid JSON permits
// backslashes only inside strings.
func loneSurrogateOffset(data []byte) (int, bool) {
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		unit, isUnicode := unicodeEscape(data, i)
		if !isUnicode {
			i++
			continue
		}
		if !utf16.IsSurrogate(unit) {
			i += unicodeEscapeLength - 1
			continue
		}
		low, hasLow := unicodeEscape(data, i+unicodeEscapeLength)
		if !hasLow || utf16.DecodeRune(unit, low) == utf8.RuneError {
			return i, true
		}
		i += 2*unicodeEscapeLength - 1
	}
	return 0, false
}

const unicodeEscapeLength = len(`\u0000`)

func unicodeEscape(data []byte, offset int) (rune, bool) {
	end := offset + unicodeEscapeLength
	if end > len(data) || data[offset] != '\\' || data[offset+1] != 'u' {
		return 0, false
	}
	unit, err := strconv.ParseUint(string(data[offset+2:end]), 16, 16)
	if err != nil {
		return 0, false
	}
	return rune(unit), true
}

func unexpectedEnd(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

func writeCanonicalValue(decoder *json.Decoder, output *bytes.Buffer) error {
	token, err := decoder.Token()
	if err != nil {
		return unexpectedEnd(err)
	}
	switch value := token.(type) {
	case json.Delim:
		if value == '{' {
			return writeCanonicalObject(decoder, output)
		}
		if value == '[' {
			return writeCanonicalArray(decoder, output)
		}
		slog.Warn("networkjson: canonicalize found an unexpected delimiter", "delimiter", value.String())
		return fmt.Errorf("unexpected delimiter %s", value)
	case string:
		return writeCanonicalString(output, value)
	case json.Number:
		output.WriteString(value.String())
	case bool:
		if value {
			output.WriteString("true")
		} else {
			output.WriteString("false")
		}
	case nil:
		output.WriteString("null")
	default:
		slog.Warn("networkjson: canonicalize found an unexpected token", "token", token)
		return fmt.Errorf("unexpected token %v", token)
	}
	return nil
}

type canonicalMember struct {
	name  string
	value []byte
}

func writeCanonicalObject(decoder *json.Decoder, output *bytes.Buffer) error {
	var members []canonicalMember
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return unexpectedEnd(err)
		}
		name, isName := token.(string)
		if !isName {
			slog.Warn("networkjson: canonicalize found a member name that is not a string", "token", token)
			return fmt.Errorf("object member name %v is not a string", token)
		}
		if err := checkMemberName(members, name); err != nil {
			return err
		}
		var value bytes.Buffer
		if err := writeCanonicalValue(decoder, &value); err != nil {
			return err
		}
		members = append(members, canonicalMember{name: name, value: value.Bytes()})
	}
	if err := requireDelim(decoder, '}'); err != nil {
		return err
	}
	slices.SortFunc(members, func(left canonicalMember, right canonicalMember) int {
		return strings.Compare(left.name, right.name)
	})
	output.WriteByte('{')
	for i, member := range members {
		if i > 0 {
			output.WriteByte(',')
		}
		if err := writeCanonicalString(output, member.name); err != nil {
			return err
		}
		output.WriteByte(':')
		output.Write(member.value)
	}
	output.WriteByte('}')
	return nil
}

// Sorting case-variant members can change the value Decode assigns to a field
// because encoding/json matches member names case-insensitively.
func checkMemberName(members []canonicalMember, name string) error {
	for _, member := range members {
		if member.name == name {
			slog.Warn("networkjson: canonicalize found a duplicate member", "member", name)
			return fmt.Errorf("object member %q appears more than once", name)
		}
		if strings.EqualFold(member.name, name) {
			slog.Warn("networkjson: canonicalize found case-variant members", "member", name, "prior", member.name)
			return fmt.Errorf("object members %q and %q differ only in letter case", member.name, name)
		}
	}
	return nil
}

func writeCanonicalArray(decoder *json.Decoder, output *bytes.Buffer) error {
	output.WriteByte('[')
	for count := 0; decoder.More(); count++ {
		if count > 0 {
			output.WriteByte(',')
		}
		if err := writeCanonicalValue(decoder, output); err != nil {
			return err
		}
	}
	if err := requireDelim(decoder, ']'); err != nil {
		return err
	}
	output.WriteByte(']')
	return nil
}

func requireDelim(decoder *json.Decoder, want json.Delim) error {
	token, err := decoder.Token()
	if err != nil {
		return unexpectedEnd(err)
	}
	if token != want {
		slog.Warn("networkjson: canonicalize found an unexpected token", "token", token, "want", want.String())
		return fmt.Errorf("unexpected token %v, want %s", token, want)
	}
	return nil
}

func writeCanonicalString(output *bytes.Buffer, value string) error {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		slog.Error("networkjson: canonical string encoding failed", "err", err)
		return fmt.Errorf("encode string: %w", err)
	}
	output.Write(bytes.TrimSuffix(encoded.Bytes(), []byte("\n")))
	return nil
}
