package service

import "testing"

func TestResolveCredentialBinding(t *testing.T) {
	current := uint(1)
	other := uint(2)

	tests := []struct {
		name    string
		current *uint
		input   *uint
		clear   bool
		want    *uint
	}{
		{"absent keeps the current binding", &current, nil, false, &current},
		{"explicit null clears", &current, nil, true, nil},
		{"a value replaces", &current, &other, false, &other},
		{"clear wins over a value", &current, &other, true, nil},
		{"absent on an unbound server stays unbound", nil, nil, false, nil},
		{"a value binds an unbound server", nil, &other, false, &other},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveCredentialBinding(tt.current, tt.input, tt.clear)
			switch {
			case tt.want == nil && got != nil:
				t.Fatalf("got %d, want nil", *got)
			case tt.want != nil && got == nil:
				t.Fatalf("got nil, want %d", *tt.want)
			case tt.want != nil && *got != *tt.want:
				t.Fatalf("got %d, want %d", *got, *tt.want)
			}
		})
	}
}
