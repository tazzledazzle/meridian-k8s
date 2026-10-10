package s0probe

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Summary counts results by status.
type Summary struct {
	Pass, Fail, Skip int
}

// Summarize counts pass, fail and skip results.
func Summarize(rs []Result) Summary {
	var s Summary
	for _, r := range rs {
		switch r.Status {
		case "pass":
			s.Pass++
		case "fail":
			s.Fail++
		default:
			s.Skip++
		}
	}
	return s
}

// FailedGroups returns the service groups with at least one failed or
// skipped check, sorted. The S0 decision matrix reads this list: a group in
// it needs a custom backend (or a documented gap) before Karpenter can run.
func FailedGroups(rs []Result) []string {
	set := map[string]bool{}
	for _, r := range rs {
		if r.Status != "pass" {
			set[r.Group] = true
		}
	}
	out := make([]string, 0, len(set))
	for g := range set {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

// Markdown renders the per-operation table that goes into docs/s0/report.md.
func Markdown(title string, rs []Result) string {
	var b strings.Builder
	s := Summarize(rs)
	fmt.Fprintf(&b, "# %s\n\n%d passed, %d failed, %d skipped.\n\n", title, s.Pass, s.Fail, s.Skip)
	b.WriteString("| Check | Group | Status | Detail |\n|---|---|---|---|\n")
	for _, r := range rs {
		// Pipes and newlines would break the table, so flatten them.
		d := strings.NewReplacer("|", "/", "\r", " ", "\n", " ").Replace(r.Detail)
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", r.Name, r.Group, r.Status, d)
	}
	if fg := FailedGroups(rs); len(fg) > 0 {
		fmt.Fprintf(&b, "\nGroups needing a custom backend or a recorded gap: %s\n", strings.Join(fg, ", "))
	} else {
		b.WriteString("\nEvery group passed on this backend: its operations are eligible to stay on it.\n")
	}
	// Amendment A2: say plainly when the instance-lifecycle call is what failed.
	for _, r := range rs {
		if r.Name == fleetCheck && r.Status != "pass" {
			b.WriteString("\nEC2 is the swing factor: " + fleetCheck + " did not pass, so the coupled EC2 checks (which share instance state and " +
				"must move together) were not run. The EC2 fallback is needed; order of trial: floci, moto, ec2-sim. " +
				"Reference-data checks (instance types, offerings, zones, spot prices) ran independently.\n")
			break
		}
	}
	// Amendment A4: a probe pass is eligibility, not acceptance.
	b.WriteString("\n## How to read this report\n\n" +
		"A pass means the backend is eligible to serve the operation: it answered with every field the probe reads. " +
		"It does not mean Karpenter accepts the answers. Karpenter itself (criteria 1 to 3, run by hand before the approval " +
		"stop) is the oracle, and it can move an operation the probe passed to the next backend. " +
		"A skip counts as not passing.\n")
	writeFieldsTable(&b, rs)
	return b.String()
}

// writeFieldsTable lists the fields each reported check requires, so they can be compared with a -record trace.
func writeFieldsTable(b *strings.Builder, rs []Result) {
	b.WriteString("\n## Fields each check requires\n\n| Check | Required fields |\n|---|---|\n")
	for _, r := range rs {
		if f := RequiredFields[r.Name]; len(f) > 0 {
			fmt.Fprintf(b, "| %s | %s |\n", r.Name, strings.ReplaceAll(strings.Join(f, ", "), "|", "/"))
		}
	}
}

// JSON renders the results for scripts (the gate and the decision matrix).
func JSON(rs []Result) ([]byte, error) {
	// required_fields repeats the RequiredFields entries of the checks in rs, so the report is self-describing.
	fields := map[string][]string{}
	for _, r := range rs {
		if f, ok := RequiredFields[r.Name]; ok {
			fields[r.Name] = f
		}
	}
	return json.MarshalIndent(struct {
		Summary        Summary             `json:"summary"`
		FailedGroups   []string            `json:"failed_groups"`
		Results        []Result            `json:"results"`
		RequiredFields map[string][]string `json:"required_fields"`
	}{Summarize(rs), FailedGroups(rs), rs, fields}, "", "  ")
}
