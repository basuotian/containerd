/*
   Copyright The containerd Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

// Package identifiers provides common validation for identifiers and keys
// across containerd.
//
// Identifiers in containerd must be a alphanumeric, allowing limited
// underscores, dashes and dots.
//
// While the character set may be expanded in the future, identifiers
// are guaranteed to be safely used as filesystem path components.
package identifiers

import (
	"fmt"

	"github.com/basuotian/containerd/internal/lazyregexp"
	"github.com/containerd/errdefs"
)

const (
	maxLength  = 76
	alphanum   = `[A-Za-z0-9]+`
	separators = `[._-]`
)

var (
	// identifierRe defines the pattern for valid identifiers.
	identifierRe = lazyregexp.New(reAnchor(alphanum + reGroup(separators+reGroup(alphanum)) + "*"))
)

// Validate returns nil if the string s is a valid identifier.
//
// identifiers are similar to the domain name rules according to RFC 1035, section 2.3.1. However
// rules in this package are relaxed to allow numerals to follow period (".") and mixed case is
// allowed.
//
// In general identifiers that pass this validation should be safe for use as filesystem path components.
func Validate(s string) error {
	if len(s) == 0 {
		return fmt.Errorf("identifier must not be empty: %w", errdefs.ErrInvalidArgument)
	}

	if len(s) > maxLength {
		return fmt.Errorf("identifier %q greater than maximum length (%d characters): %w", s, maxLength, errdefs.ErrInvalidArgument)
	}

	if !identifierRe.MatchString(s) {
		return fmt.Errorf("identifier %q must match %v: %w", s, identifierRe, errdefs.ErrInvalidArgument)
	}
	return nil
}

func reGroup(s string) string {
	return `(?:` + s + `)`
}

func reAnchor(s string) string {
	return `^` + s + `$`
}
