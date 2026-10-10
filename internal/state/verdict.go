// verdict.go parses `.afk/{name}/review-verdict.md`, per the schema
// recorded in .afk/afk-go-orchestration/decisions.md under "[W1.T4]
// review-verdict.md schema" (claude-marche checkout) — that decision is the
// authoritative shape, since no standalone schema doc exists for this file
// the way plan-format.md and status-schema.md exist for the other two:
//
//	# Review Verdict
//
//	**Plan**: {plan-name}
//	**Date**: {today}
//	**Verdict**: APPROVE | REJECT
//
//	## Checks
//	- Compliance: PASS | FAIL
//	- Quality: PASS | FAIL
//	- Scope: PASS | FAIL
//
//	## Findings
//	- [{severity}] {task-id}: {one-line description}
//
// severity is one of blocker|major|minor; task-id is a plan task ID (e.g.
// W2.T1) or the literal "general"; an empty Findings section is rendered as
// a single "- none" line.
package state

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Verdict is the reviewer's overall APPROVE/REJECT decision.
type Verdict string

const (
	VerdictApprove Verdict = "APPROVE"
	VerdictReject  Verdict = "REJECT"
)

// CheckResult is a PASS/FAIL outcome for one of the three named checks.
type CheckResult string

const (
	CheckPass CheckResult = "PASS"
	CheckFail CheckResult = "FAIL"
)

// Severity is a finding's severity level.
type Severity string

const (
	SeverityBlocker Severity = "blocker"
	SeverityMajor   Severity = "major"
	SeverityMinor   Severity = "minor"
)

// Finding is one `- [{severity}] {task-id}: {description}` line.
type Finding struct {
	Severity    Severity
	TaskID      string // e.g. "W2.T1", or the literal "general"
	Description string
}

// ReviewVerdict is the fully-parsed contents of a review-verdict.md file.
type ReviewVerdict struct {
	Plan       string
	Date       string
	Verdict    Verdict
	Compliance CheckResult
	Quality    CheckResult
	Scope      CheckResult
	// Findings is empty when the file's Findings section was the literal
	// "- none" line — never nil vs. empty-slice significant to callers.
	Findings []Finding
}

// Consistent reports whether the stated Verdict matches the three Checks,
// per the recorded rule "Verdict is APPROVE only if all three Checks are
// PASS." This is a read-only diagnostic helper for callers (e.g. the
// dashboard) that want to flag an inconsistent verdict file; ParseVerdict
// itself does not reject inconsistent-but-well-formed files, since the
// parser's job is to faithfully report what the reviewer wrote, not to
// re-adjudicate it.
func (v *ReviewVerdict) Consistent() bool {
	allPass := v.Compliance == CheckPass && v.Quality == CheckPass && v.Scope == CheckPass
	if v.Verdict == VerdictApprove {
		return allPass
	}
	return !allPass
}

var (
	metaRE    = regexp.MustCompile(`^\*\*(Plan|Date|Verdict)\*\*:\s*(.+)$`)
	checkRE   = regexp.MustCompile(`^- (Compliance|Quality|Scope):\s*(PASS|FAIL)\s*$`)
	findingRE = regexp.MustCompile(`^- \[(blocker|major|minor)\]\s*([^:]+):\s*(.+)$`)
	noneRE    = regexp.MustCompile(`^- none\s*$`)
	headingRE = regexp.MustCompile(`^## (.+)$`)
)

// ParseVerdictFile reads and parses the review-verdict.md file at path.
func ParseVerdictFile(path string) (*ReviewVerdict, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading review verdict file %s: %w", path, err)
	}
	v, err := ParseVerdict(data)
	if err != nil {
		return nil, fmt.Errorf("parsing review verdict file %s: %w", path, err)
	}
	return v, nil
}

// ParseVerdict parses raw review-verdict.md markdown. It never panics;
// malformed input (missing required fields, an unrecognized Verdict/PASS-
// FAIL/severity value, a findings line that doesn't match the schema) is
// surfaced as an error with a line number.
func ParseVerdict(data []byte) (*ReviewVerdict, error) {
	v := &ReviewVerdict{}
	section := ""
	foundNoneOrFinding := false

	lines := strings.Split(string(data), "\n")
	for i, raw := range lines {
		lineNo := i + 1
		line := strings.TrimRight(raw, " \t\r")
		if strings.TrimSpace(line) == "" {
			continue
		}

		if m := headingRE.FindStringSubmatch(line); m != nil {
			section = strings.TrimSpace(m[1])
			continue
		}

		if m := metaRE.FindStringSubmatch(line); m != nil {
			val := strings.TrimSpace(m[2])
			switch m[1] {
			case "Plan":
				v.Plan = val
			case "Date":
				v.Date = val
			case "Verdict":
				switch Verdict(val) {
				case VerdictApprove, VerdictReject:
					v.Verdict = Verdict(val)
				default:
					return nil, fmt.Errorf("line %d: invalid Verdict %q (must be APPROVE or REJECT)", lineNo, val)
				}
			}
			continue
		}

		if section == "Checks" {
			if m := checkRE.FindStringSubmatch(line); m != nil {
				result := CheckResult(m[2])
				switch m[1] {
				case "Compliance":
					v.Compliance = result
				case "Quality":
					v.Quality = result
				case "Scope":
					v.Scope = result
				}
				continue
			}
			return nil, fmt.Errorf("line %d: unrecognized line in ## Checks section: %q", lineNo, line)
		}

		if section == "Findings" {
			if noneRE.MatchString(line) {
				foundNoneOrFinding = true
				continue
			}
			if m := findingRE.FindStringSubmatch(line); m != nil {
				v.Findings = append(v.Findings, Finding{
					Severity:    Severity(m[1]),
					TaskID:      strings.TrimSpace(m[2]),
					Description: strings.TrimSpace(m[3]),
				})
				foundNoneOrFinding = true
				continue
			}
			return nil, fmt.Errorf("line %d: unrecognized line in ## Findings section: %q (expected \"- [severity] task-id: description\" or \"- none\")", lineNo, line)
		}
	}

	if err := validateVerdict(v, foundNoneOrFinding); err != nil {
		return nil, err
	}
	return v, nil
}

// validateVerdict enforces the required-fields rules that only make sense
// once the whole file has been seen.
func validateVerdict(v *ReviewVerdict, foundFindingsSection bool) error {
	if v.Plan == "" {
		return fmt.Errorf("missing required **Plan** field")
	}
	if v.Verdict == "" {
		return fmt.Errorf("missing required **Verdict** field")
	}
	if v.Compliance == "" || v.Quality == "" || v.Scope == "" {
		return fmt.Errorf("## Checks section is incomplete: Compliance/Quality/Scope must all be present")
	}
	if !foundFindingsSection {
		return fmt.Errorf("## Findings section is missing or empty (expected at least a \"- none\" line)")
	}
	return nil
}
