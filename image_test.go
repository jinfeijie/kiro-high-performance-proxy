package kiroclient

import "testing"

func TestParseDataURL(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantFormat string
		wantData   string
		wantOK     bool
	}{
		{
			name:       "PNG 图片",
			input:      "data:image/png;base64,iVBORw0KGgo=",
			wantFormat: "png",
			wantData:   "iVBORw0KGgo=",
			wantOK:     true,
		},
		{
			name:       "JPEG 图片",
			input:      "data:image/jpeg;base64,/9j/4AAQ=",
			wantFormat: "jpeg",
			wantData:   "/9j/4AAQ=",
			wantOK:     true,
		},
		{
			name:       "WebP 图片",
			input:      "data:image/webp;base64,UklGR=",
			wantFormat: "webp",
			wantData:   "UklGR=",
			wantOK:     true,
		},
		{
			name:       "GIF 图片",
			input:      "data:image/gif;base64,R0lGOD=",
			wantFormat: "gif",
			wantData:   "R0lGOD=",
			wantOK:     true,
		},
		{
			name:   "无效前缀",
			input:  "http://example.com/image.png",
			wantOK: false,
		},
		{
			name:   "空字符串",
			input:  "",
			wantOK: false,
		},
		{
			name:   "非图片 MIME",
			input:  "data:text/plain;base64,SGVsbG8=",
			wantOK: false,
		},
		{
			name:   "缺少 base64 标记",
			input:  "data:image/png,iVBORw0KGgo=",
			wantOK: false,
		},
		{
			name:   "空数据",
			input:  "data:image/png;base64,",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			format, data, ok := ParseDataURL(tt.input)
			if ok != tt.wantOK {
				t.Errorf("ok = %v, want %v", ok, tt.wantOK)
				return
			}
			if !tt.wantOK {
				return
			}
			if format != tt.wantFormat {
				t.Errorf("format = %q, want %q", format, tt.wantFormat)
			}
			if data != tt.wantData {
				t.Errorf("data = %q, want %q", data, tt.wantData)
			}
		})
	}
}
