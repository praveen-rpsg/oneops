package arch_test

import "testing"

func TestViolations(t *testing.T) {
	tests := []struct {
		name string
		pkgs []pkg
		rule rule
		want int
	}{
		{
			name: "clean domain produces no violation",
			pkgs: []pkg{{ImportPath: module + "internal/domain", Imports: []string{"context"}}},
			rule: rule{From: module + "internal/domain", Forbidden: []string{module}},
			want: 0,
		},
		{
			name: "domain importing a repo package is caught",
			pkgs: []pkg{{ImportPath: module + "internal/domain", Imports: []string{module + "internal/audit"}}},
			rule: rule{From: module + "internal/domain", Forbidden: []string{module}},
			want: 1,
		},
		{
			name: "self-import via the external test package is not a violation",
			pkgs: []pkg{{ImportPath: module + "sdk", XTestImports: []string{module + "sdk"}}},
			rule: rule{From: module + "sdk", Forbidden: []string{module + "internal/"}, IncludeTests: true},
			want: 0,
		},
		{
			name: "test import ignored when IncludeTests is false",
			pkgs: []pkg{{ImportPath: module + "internal/httpapi", TestImports: []string{module + "internal/store/postgres"}}},
			rule: rule{From: module + "internal/httpapi", Forbidden: []string{module + "internal/store/"}},
			want: 0,
		},
		{
			name: "test import caught when IncludeTests is true",
			pkgs: []pkg{{ImportPath: module + "internal/httpapi", TestImports: []string{module + "internal/store/postgres"}}},
			rule: rule{From: module + "internal/httpapi", Forbidden: []string{module + "internal/store/"}, IncludeTests: true},
			want: 1,
		},
		{
			name: "prefix match does not catch a sibling with a shared prefix",
			pkgs: []pkg{{ImportPath: module + "internal/domainx", Imports: []string{module + "internal/audit"}}},
			rule: rule{From: module + "internal/domain", Forbidden: []string{module}},
			want: 0,
		},
		{
			name: "allow list exempts an exact path",
			pkgs: []pkg{{ImportPath: module + "internal/domain", Imports: []string{module + "internal/audit"}}},
			rule: rule{From: module + "internal/domain", Forbidden: []string{module}, Allow: []string{module + "internal/audit"}},
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := len(violations(tt.pkgs, tt.rule)); got != tt.want {
				t.Errorf("violations() = %d, want %d", got, tt.want)
			}
		})
	}
}
