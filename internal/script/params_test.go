package script

import (
	"reflect"
	"testing"

	"github.com/cntunglam/coop/internal/config"
)

func TestParamNames(t *testing.T) {
	cases := []struct {
		name string
		s    config.Script
		want []string
	}{
		{
			name: "from command template",
			s:    config.Script{Command: "ssh {{.user}}@{{.host}} -p {{.port}}"},
			want: []string{"host", "port", "user"},
		},
		{
			name: "includes ready_port var",
			s:    config.Script{Command: "serve {{.port}}", ReadyPort: "{{.port}}"},
			want: []string{"port"},
		},
		{
			name: "unions existing preset keys",
			s: config.Script{
				Command: "run {{.a}}",
				Presets: map[string]map[string]string{"p": {"a": "1", "b": "2"}},
			},
			want: []string{"a", "b"},
		},
		{
			name: "spaces inside braces",
			s:    config.Script{Command: "x {{ .foo }}"},
			want: []string{"foo"},
		},
		{
			name: "no params",
			s:    config.Script{Command: "echo hi"},
			want: []string{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ParamNames(c.s)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("ParamNames = %v, want %v", got, c.want)
			}
		})
	}
}
