package runtime

import "testing"

func TestDaemonUnavailable(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want bool
	}{
		{
			name: "socket permission denied",
			out:  "permission denied while trying to connect to the docker API at unix:///var/run/docker.sock",
			want: true,
		},
		{
			name: "daemon not running",
			out:  "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?",
			want: true,
		},
		{
			name: "container missing",
			out:  "Error response from daemon: No such container: clab-dc1-leaf-1",
			want: false,
		},
		{
			name: "command failed inside container",
			out:  "vtysh: command not found",
			want: false,
		},
		{
			name: "empty output",
			out:  "",
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := daemonUnavailable([]byte(tc.out)); got != tc.want {
				t.Errorf("daemonUnavailable(%q) = %v, want %v", tc.out, got, tc.want)
			}
		})
	}
}
