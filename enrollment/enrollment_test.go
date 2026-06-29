package enrollment

import "testing"

func TestIsSessionExpiredResponse(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "undefined", body: "undefined", want: true},
		{name: "undefined with whitespace", body: " \nundefined\t", want: true},
		{name: "logout indicator", body: "<html>注销</html>", want: true},
		{name: "normal success", body: `{"success":[true],"message":"成功"}`, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSessionExpiredResponse(tt.body); got != tt.want {
				t.Fatalf("IsSessionExpiredResponse() = %v, want %v", got, tt.want)
			}
		})
	}
}
