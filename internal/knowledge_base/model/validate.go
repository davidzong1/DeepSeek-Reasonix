package model

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

const (
	maxTitleRunes = 120
	maxBodyBytes  = 8192
	maxTagRunes   = 64
)

var (
	// teamIDRe and relIDRe confine on-disk names to a single safe path segment.
	teamIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	relIDRe  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

	ErrInvalid = errors.New("knowledge_base: invalid value")
)

// ValidateTeamID rejects ids that could escape the team data directory.
func ValidateTeamID(id string) error {
	if !teamIDRe.MatchString(id) {
		return fmt.Errorf("%w: team id %q must match %s", ErrInvalid, id, teamIDRe)
	}
	return nil
}

// ValidateRelID is the shared path-segment check for item and job ids.
func ValidateRelID(id string) error {
	if !relIDRe.MatchString(id) {
		return fmt.Errorf("%w: id %q must match %s", ErrInvalid, id, relIDRe)
	}
	return nil
}

func validRef(r Ref) bool {
	return strings.TrimSpace(r.Kind) != "" && strings.TrimSpace(r.Target) != ""
}

// Validate enforces the model invariant set; it never mutates the item.
func ValidateItem(i KnowledgeItem) error {
	if err := ValidateRelID(i.ID); err != nil {
		return err
	}
	if n := len([]rune(i.Title)); n == 0 || n > maxTitleRunes {
		return fmt.Errorf("%w: title rune count %d out of (0,%d]", ErrInvalid, n, maxTitleRunes)
	}
	if len(i.Body) > maxBodyBytes {
		return fmt.Errorf("%w: body %d bytes exceeds %d", ErrInvalid, len(i.Body), maxBodyBytes)
	}
	if !i.Kind.Valid() {
		return fmt.Errorf("%w: item kind %q", ErrInvalid, i.Kind)
	}
	if !i.Scope.Valid() {
		return fmt.Errorf("%w: scope %q", ErrInvalid, i.Scope)
	}
	if !i.Status.Valid() {
		return fmt.Errorf("%w: status %q", ErrInvalid, i.Status)
	}
	if !i.Quality.ReviewLevel.Valid() {
		return fmt.Errorf("%w: review level %q", ErrInvalid, i.Quality.ReviewLevel)
	}
	if i.Version < 1 {
		return fmt.Errorf("%w: version %d must be >= 1", ErrInvalid, i.Version)
	}
	provenanced := false
	for _, r := range i.Provenance {
		if validRef(r) {
			provenanced = true
		}
	}
	if !provenanced {
		return errors.New("knowledge_base: at least one non-empty provenance target required")
	}
	if CanonicalKey(i.Scope, i.Kind, i.Title) != i.Canonical {
		return fmt.Errorf("%w: canonical %q does not match scope:kind:title", ErrInvalid, i.Canonical)
	}
	for _, t := range i.Tags {
		if len([]rune(t)) > maxTagRunes {
			return fmt.Errorf("%w: tag exceeds %d runes", ErrInvalid, maxTagRunes)
		}
	}
	return nil
}

// CanonicalKey is the L2 dedup key: same canonical means same knowledge slot.
func CanonicalKey(scope Scope, kind ItemKind, title string) string {
	return string(scope) + ":" + string(kind) + ":" + Slug(title)
}

// Slug lowercases and collapses non-alphanumeric runs into a single hyphen.
func Slug(s string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastDash = false
		case !lastDash:
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
