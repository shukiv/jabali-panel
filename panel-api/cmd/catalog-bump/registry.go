package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// maxTagPages bounds tags/list pagination (Docker Hub's ghost repo has
// thousands of tags). Hitting the cap is reported, never silent.
const maxTagPages = 50

const manifestAccept = "application/vnd.oci.image.index.v1+json, " +
	"application/vnd.docker.distribution.manifest.list.v2+json, " +
	"application/vnd.oci.image.manifest.v1+json, " +
	"application/vnd.docker.distribution.manifest.v2+json"

// regClient talks the OCI distribution API against any registry, discovering
// the token endpoint from the /v2/ WWW-Authenticate challenge (works for
// Docker Hub, ghcr.io, lscr.io, codeberg.org alike).
type regClient struct {
	http *http.Client
	// scheme is overridable for httptest servers ("https" in production).
	scheme string
	tokens map[string]string // host|repo → bearer token
}

func newRegClient() *regClient {
	return &regClient{
		http:   &http.Client{Timeout: 30 * time.Second},
		scheme: "https",
		tokens: map[string]string{},
	}
}

var challengeFieldRe = regexp.MustCompile(`(\w+)="([^"]*)"`)

// token performs anonymous pull-scope auth for host/repo. Registries that
// answer /v2/ with 200 need no token; the empty string means exactly that.
func (c *regClient) token(host, repo string) (string, error) {
	key := host + "|" + repo
	if t, ok := c.tokens[key]; ok {
		return t, nil
	}
	resp, err := c.http.Get(c.scheme + "://" + host + "/v2/")
	if err != nil {
		return "", fmt.Errorf("probe %s: %w", host, err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		c.tokens[key] = ""
		return "", nil
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return "", fmt.Errorf("probe %s: unexpected status %d", host, resp.StatusCode)
	}
	challenge := resp.Header.Get("Www-Authenticate")
	if !strings.HasPrefix(challenge, "Bearer ") {
		return "", fmt.Errorf("probe %s: unsupported auth challenge %q", host, challenge)
	}
	fields := map[string]string{}
	for _, m := range challengeFieldRe.FindAllStringSubmatch(challenge, -1) {
		fields[m[1]] = m[2]
	}
	realm := fields["realm"]
	if realm == "" {
		return "", fmt.Errorf("probe %s: challenge without realm", host)
	}
	q := url.Values{}
	if s := fields["service"]; s != "" {
		q.Set("service", s)
	}
	q.Set("scope", "repository:"+repo+":pull")
	tokResp, err := c.http.Get(realm + "?" + q.Encode())
	if err != nil {
		return "", fmt.Errorf("token %s: %w", host, err)
	}
	defer tokResp.Body.Close()
	if tokResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token %s: status %d", host, tokResp.StatusCode)
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(tokResp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("token %s: %w", host, err)
	}
	tok := body.Token
	if tok == "" {
		tok = body.AccessToken
	}
	if tok == "" {
		return "", fmt.Errorf("token %s: empty token", host)
	}
	c.tokens[key] = tok
	return tok, nil
}

func (c *regClient) get(host, repo, path string, head bool, accept string) (*http.Response, error) {
	tok, err := c.token(host, repo)
	if err != nil {
		return nil, err
	}
	method := http.MethodGet
	if head {
		method = http.MethodHead
	}
	u := path
	if !strings.HasPrefix(u, c.scheme+"://") {
		u = c.scheme + "://" + host + path
	}
	req, err := http.NewRequest(method, u, nil)
	if err != nil {
		return nil, err
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	return c.http.Do(req)
}

// tags lists every tag for host/repo, following RFC 5988 Link pagination.
// truncated=true means the page cap was hit and the list is incomplete.
func (c *regClient) tags(host, repo string) (tags []string, truncated bool, err error) {
	path := "/v2/" + repo + "/tags/list?n=1000"
	for page := 0; page < maxTagPages; page++ {
		resp, err := c.get(host, repo, path, false, "")
		if err != nil {
			return nil, false, fmt.Errorf("tags %s/%s: %w", host, repo, err)
		}
		if resp.StatusCode != http.StatusOK {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return nil, false, fmt.Errorf("tags %s/%s: status %d", host, repo, resp.StatusCode)
		}
		var body struct {
			Tags []string `json:"tags"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&body)
		link := resp.Header.Get("Link")
		resp.Body.Close()
		if decodeErr != nil {
			return nil, false, fmt.Errorf("tags %s/%s: %w", host, repo, decodeErr)
		}
		tags = append(tags, body.Tags...)
		next := parseNextLink(link)
		if next == "" {
			return tags, false, nil
		}
		path = next
	}
	return tags, true, nil
}

var linkRe = regexp.MustCompile(`<([^>]+)>\s*;[^,]*rel="next"`)

func parseNextLink(link string) string {
	m := linkRe.FindStringSubmatch(link)
	if m == nil {
		return ""
	}
	return m[1]
}

// digest resolves the manifest digest for a tag via a HEAD request.
func (c *regClient) digest(host, repo, tag string) (string, error) {
	resp, err := c.get(host, repo, "/v2/"+repo+"/manifests/"+tag, true, manifestAccept)
	if err != nil {
		return "", fmt.Errorf("digest %s/%s:%s: %w", host, repo, tag, err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("digest %s/%s:%s: status %d", host, repo, tag, resp.StatusCode)
	}
	d := resp.Header.Get("Docker-Content-Digest")
	if !digestRe.MatchString(d) {
		return "", fmt.Errorf("digest %s/%s:%s: bad digest header %q", host, repo, tag, d)
	}
	return d, nil
}
