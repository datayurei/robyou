package parser

import "testing"

const casLoginPage = `<html><body><form id="fm1" action="/login" method="post">
	<input id="username" name="username" type="text"/>
	<input id="password" name="password" type="password"/>
	<input type="hidden" name="lt" value="LT-987-xyz"/>
	<input type="hidden" name="execution" value="e1s1"/>
	<input type="hidden" name="_eventId" value="submit"/>
</form></body></html>`

const jwPortalPage = `<html><body><div id="menu">
	<a href="/jsxsd/xsxk/xklc_list?Ves632DSdyV=NEW_XSD_PYGL">学生选课</a>
	<a href="/jsxsd/xk/LoginToXk?method=exit">注销</a>
</div></body></html>`

func TestIsLoginPage(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "cas login form", body: casLoginPage, want: true},
		{name: "login form without lt", body: `<form><input name="password"/><input name="_eventId"/></form>`, want: true},
		{name: "portal page", body: jwPortalPage, want: false},
		{name: "json response", body: `{"aaData":[]}`, want: false},
		{name: "empty", body: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsLoginPage(tt.body); got != tt.want {
				t.Fatalf("IsLoginPage() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractLtFromLogin(t *testing.T) {
	lt, ok := ExtractLtFromLogin(casLoginPage)
	if !ok || lt != "LT-987-xyz" {
		t.Fatalf("ExtractLtFromLogin() = %q / %v", lt, ok)
	}

	if _, ok := ExtractLtFromLogin(jwPortalPage); ok {
		t.Fatal("the portal page has no login ticket")
	}
}

func TestExtractXklc(t *testing.T) {
	url, ok := ExtractXklc(jwPortalPage)
	if !ok {
		t.Fatal("the portal page has a round link")
	}
	if url != "https://jw.stu.edu.cn/jsxsd/xsxk/xklc_list?Ves632DSdyV=NEW_XSD_PYGL" {
		t.Fatalf("url = %q", url)
	}

	// Before enrollment opens the link is absent entirely.
	if _, ok := ExtractXklc(`<html><body><a href="/jsxsd/xsxx/xsxxxx">学生信息</a></body></html>`); ok {
		t.Fatal("a portal without a round link must report false")
	}
}

func TestExtractXklcWithBase(t *testing.T) {
	url, ok := ExtractXklcWithBase(jwPortalPage, "http://127.0.0.1:8080/")
	if !ok || url != "http://127.0.0.1:8080/jsxsd/xsxk/xklc_list?Ves632DSdyV=NEW_XSD_PYGL" {
		t.Fatalf("ExtractXklcWithBase() = %q / %v", url, ok)
	}
}

func TestExtractXkid(t *testing.T) {
	xkid, ok := ExtractXkid(`<a href="newXsxkzx?jx0502zbid=A1B2C3D4E5F60718293A4B5C6D7E8F90">选课</a>`)
	if !ok || xkid != "A1B2C3D4E5F60718293A4B5C6D7E8F90" {
		t.Fatalf("ExtractXkid() = %q / %v", xkid, ok)
	}

	// An empty round list has no 32-hex id in it.
	if _, ok := ExtractXkid(`<table id="tbList"><tbody></tbody></table>`); ok {
		t.Fatal("an empty round list must report false")
	}
}
