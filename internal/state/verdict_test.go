package state

import (
	"path/filepath"
	"testing"
)

func TestParseVerdictFile_Approve(t *testing.T) {
	v, err := ParseVerdictFile(filepath.Join("testdata", "verdict", "approve.md"))
	if err != nil {
		t.Fatalf("ParseVerdictFile: %v", err)
	}
	if v.Plan != "turt-1234-add-billing-webhook" {
		t.Errorf("Plan = %q", v.Plan)
	}
	if v.Date != "2026-07-09" {
		t.Errorf("Date = %q", v.Date)
	}
	if v.Verdict != VerdictApprove {
		t.Errorf("Verdict = %q, want APPROVE", v.Verdict)
	}
	if v.Compliance != CheckPass || v.Quality != CheckPass || v.QA != CheckPass {
		t.Errorf("Checks = %+v, want all PASS", v)
	}
	if len(v.Findings) != 0 {
		t.Errorf("Findings = %+v, want empty (the file said \"- none\")", v.Findings)
	}
	if !v.Consistent() {
		t.Errorf("Consistent() = false, want true (APPROVE with all-PASS checks)")
	}
}

func TestParseVerdictFile_RejectWithFindings(t *testing.T) {
	v, err := ParseVerdictFile(filepath.Join("testdata", "verdict", "reject.md"))
	if err != nil {
		t.Fatalf("ParseVerdictFile: %v", err)
	}
	if v.Verdict != VerdictReject {
		t.Errorf("Verdict = %q, want REJECT", v.Verdict)
	}
	if v.Compliance != CheckFail || v.Quality != CheckPass || v.QA != CheckPass {
		t.Errorf("Checks = %+v", v)
	}
	if len(v.Findings) != 3 {
		t.Fatalf("len(Findings) = %d, want 3", len(v.Findings))
	}

	want := []Finding{
		{Severity: SeverityBlocker, TaskID: "W2.T1", Description: "PATCH endpoint accepts unknown fields without a 400"},
		{Severity: SeverityMajor, TaskID: "W3.T2", Description: "Notification dispatcher does not log skipped sends"},
		{Severity: SeverityMinor, TaskID: "general", Description: "Missing changelog entry for the new endpoint"},
	}
	for i, f := range want {
		if v.Findings[i] != f {
			t.Errorf("Findings[%d] = %+v, want %+v", i, v.Findings[i], f)
		}
	}
	if !v.Consistent() {
		t.Errorf("Consistent() = false, want true (REJECT with a FAIL check)")
	}
}

func TestParseVerdictFile_Malformed(t *testing.T) {
	tests := []struct {
		name string
		file string
	}{
		{"missing Verdict field", "malformed_missing_verdict.md"},
		{"invalid severity", "malformed_invalid_severity.md"},
		{"incomplete Checks section", "malformed_incomplete_checks.md"},
		{"missing Findings section", "malformed_missing_findings.md"},
		// Load-bearing: no compatibility path for the old Scope line.
		{"legacy Scope line", "malformed_legacy_scope.md"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := ParseVerdictFile(filepath.Join("testdata", "verdict", tt.file))
			if err == nil {
				t.Fatalf("ParseVerdictFile(%s) = %+v, nil; want a non-nil error", tt.file, v)
			}
			if v != nil {
				t.Errorf("ParseVerdictFile(%s) returned a non-nil verdict alongside an error", tt.file)
			}
		})
	}
}

func TestParseVerdictFile_MissingFile(t *testing.T) {
	_, err := ParseVerdictFile(filepath.Join("testdata", "verdict", "does-not-exist.md"))
	if err == nil {
		t.Fatal("expected an error for a missing verdict file, got nil")
	}
}

func TestParseVerdict_EmptyInputDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ParseVerdict panicked on empty input: %v", r)
		}
	}()
	if _, err := ParseVerdict([]byte("")); err == nil {
		t.Fatal("expected an error for empty input, got nil")
	}
}

func TestReviewVerdict_ConsistentDetectsMismatch(t *testing.T) {
	v := &ReviewVerdict{
		Verdict:    VerdictApprove,
		Compliance: CheckPass,
		Quality:    CheckFail,
		QA:         CheckPass,
	}
	if v.Consistent() {
		t.Error("Consistent() = true, want false (APPROVE with a FAIL check is inconsistent)")
	}
}
