package main

import (
	"fmt"
	"net/url"

	"github.com/datayurei/robyou/httpclient"
	"github.com/datayurei/robyou/parser"
)

type loginInfo struct {
	username string
	password string
}

// build a global http client
var globalClient *httpclient.Client

func main() {
	globalClient = httpclient.New()

	info := loginInfo{username: "12abcde3",
		password: "123123",
	}

	// login with account and passwrod
	login(globalClient, info)
	// shareCookiesAcrossSubdomains(globalClient)

	xkURL,_ := getXklc(globalClient)

	fmt.Println(xkURL)
	fmt.Println(parser.CheckLoginStatus(globalClient))

}

// login function will login the client by loginInfo on sso.stu.edu.cn
func login(client *httpclient.Client, info loginInfo) (bool, error) {
	loginURL := "https://sso.stu.edu.cn/login?service=http%3A%2F%2Fjw.stu.edu.cn%2F"

	html, _ := client.GetString(loginURL)

	lt, _ := parser.ExtractLtFromLogin(html)

	loginData := url.Values{
		"username":  {info.username},
		"password":  {info.password},
		"lt":        {lt},
		"execution": {"e1s1"},
		"_eventId":  {"submit"},
	}

	client.PostFormString(loginURL, loginData)

	isLogin := parser.CheckLoginStatus(client)
	if !isLogin {
		return false, fmt.Errorf("auth failed, check your account and password")
	}

	return true, nil

}

// get courses selecton round
func getXklc(client *httpclient.Client) (string, bool) {
	resp, _ := client.GetString("https://jw.stu.edu.cn/jsxsd/framework/xsrkxz.htmlx")
	URL, _ := parser.ExtractXklc(resp)
	return URL, false
}
