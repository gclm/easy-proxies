package proxyshare

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"easy_proxies/internal/config"
)

const (
	defaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/132.0.0.0 Safari/537.36"

	v2nodesHomeURL = "https://www.v2nodes.com/"
	cnc07APIURL    = "http://cnc07api.cnc07.com/api/cnc07iuapis"
)

var shadowshareURLs = []string{
	"https://gitee.com/api/v5/repos/configshare/share/raw/%s?access_token=9019dae4f65bd15afba8888f95d7ebcc&ref=hotfix",
	"https://raw.githubusercontent.com/configshare/share/hotfix/%s",
	"https://shadowshare.v2cross.com/servers/%s",
}

const (
	shadowshareKey = "8YfiQ8wrkziZ5YFW"
	shadowshareIV  = "8YfiQ8wrkziZ5YFW"
	cnc07Key       = "1kv10h7t*C3f8c@$"
	cnc07IV        = "@$6l&bxb5n35c2w9"
)

// Options controls proxy_share discovery behavior.
type Options struct {
	UserAgent string
}

// Stats summarizes proxy_share node collection.
type Stats struct {
	SourcesTotal     int               `json:"sources_total"`
	SourcesSucceeded int               `json:"sources_succeeded"`
	SourcesFailed    int               `json:"sources_failed"`
	SourceNodes      map[string]int    `json:"source_nodes,omitempty"`
	SourceErrors     map[string]string `json:"source_errors,omitempty"`
	DuplicateNodes   int               `json:"duplicate_nodes"`
	TotalNodes       int               `json:"total_nodes"`
}

type sourceDef struct {
	Name string
	Func func(ctx context.Context, client *http.Client, ua string) (string, error)
}

// DiscoverNodes fetches node sources and returns deduplicated node URIs.
func DiscoverNodes(ctx context.Context, client *http.Client, opts Options) ([]string, Stats, error) {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	if strings.TrimSpace(opts.UserAgent) == "" {
		opts.UserAgent = defaultUserAgent
	}

	stats := Stats{
		SourceNodes:  make(map[string]int),
		SourceErrors: make(map[string]string),
	}
	sources := []sourceDef{
		{Name: "v2nodes", Func: fetchV2NodesContent},
		{Name: "shadowshare_server", Func: func(ctx context.Context, client *http.Client, ua string) (string, error) {
			return fetchShadowshareContent(ctx, client, ua, "shadowshareserver")
		}},
		{Name: "shadowshare_clash_http", Func: func(ctx context.Context, client *http.Client, ua string) (string, error) {
			return fetchShadowshareContent(ctx, client, ua, "clash_http_encrypt")
		}},
		{Name: "shadowshare_clash_https", Func: func(ctx context.Context, client *http.Client, ua string) (string, error) {
			return fetchShadowshareContent(ctx, client, ua, "clash_https_encrypt")
		}},
		{Name: "shadowshare_clash_socks5", Func: func(ctx context.Context, client *http.Client, ua string) (string, error) {
			return fetchShadowshareContent(ctx, client, ua, "clash_socks5_encrypt")
		}},
		{Name: "cnc07", Func: fetchCNC07Content},
	}
	stats.SourcesTotal = len(sources)

	seen := make(map[string]struct{})
	nodes := make([]string, 0, 1024)

	for _, src := range sources {
		content, err := src.Func(ctx, client, opts.UserAgent)
		if err != nil {
			stats.SourcesFailed++
			stats.SourceErrors[src.Name] = err.Error()
			continue
		}
		parsed, err := config.ParseSubscriptionContent(content)
		if err != nil {
			stats.SourcesFailed++
			stats.SourceErrors[src.Name] = err.Error()
			continue
		}
		stats.SourcesSucceeded++
		stats.SourceNodes[src.Name] = len(parsed)
		for _, node := range parsed {
			uri := strings.TrimSpace(node.URI)
			if uri == "" {
				continue
			}
			if _, ok := seen[uri]; ok {
				stats.DuplicateNodes++
				continue
			}
			seen[uri] = struct{}{}
			nodes = append(nodes, uri)
		}
	}

	stats.TotalNodes = len(nodes)
	return nodes, stats, nil
}

func fetchV2NodesContent(ctx context.Context, client *http.Client, ua string) (string, error) {
	page, err := httpGet(ctx, client, v2nodesHomeURL, ua)
	if err != nil {
		return "", err
	}
	re := regexp.MustCompile(`data-config="(.*?)"`)
	matches := re.FindStringSubmatch(page)
	if len(matches) < 2 {
		return "", fmt.Errorf("v2nodes: failed to extract subscription link")
	}
	subURL := strings.TrimSpace(matches[1])
	if strings.HasPrefix(subURL, "//") {
		subURL = "https:" + subURL
	}
	if strings.HasPrefix(subURL, "/") {
		subURL = "https://www.v2nodes.com" + subURL
	}

	content, err := httpGet(ctx, client, subURL, ua)
	if err != nil {
		return "", err
	}
	decoded, err := decodeBase64Loose(content)
	if err != nil {
		return "", fmt.Errorf("v2nodes: decode base64: %w", err)
	}
	return string(decoded), nil
}

func fetchShadowshareContent(ctx context.Context, client *http.Client, ua, file string) (string, error) {
	var lastErr error
	for _, tmpl := range shadowshareURLs {
		raw, err := httpGet(ctx, client, fmt.Sprintf(tmpl, file), ua)
		if err != nil {
			lastErr = err
			continue
		}
		plain, err := aesDecryptCBCBase64(raw, shadowshareKey, shadowshareIV)
		if err != nil {
			lastErr = err
			continue
		}
		return plain, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("unknown error")
	}
	return "", fmt.Errorf("shadowshare %s: %w", file, lastErr)
}

func fetchCNC07Content(ctx context.Context, client *http.Client, ua string) (string, error) {
	respBody, err := httpGet(ctx, client, cnc07APIURL, ua)
	if err != nil {
		return "", err
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(respBody), &payload); err != nil {
		return "", fmt.Errorf("cnc07: decode response: %w", err)
	}
	servers, _ := payload["servers"].(string)
	if strings.TrimSpace(servers) == "" {
		return "", fmt.Errorf("cnc07: missing servers field")
	}

	plain, err := aesDecryptCBCBase64(servers, cnc07Key, cnc07IV)
	if err != nil {
		return "", fmt.Errorf("cnc07: decrypt servers: %w", err)
	}

	var configs []map[string]any
	if err := json.Unmarshal([]byte(plain), &configs); err != nil {
		return "", fmt.Errorf("cnc07: decode config list: %w", err)
	}

	re := regexp.MustCompile(`SS = ss, ([\d.]+), (\d+),encrypt-method=([\w-]+),password=([\w\d]+)`)
	var out strings.Builder
	for _, item := range configs {
		alias, _ := item["alias"].(string)
		matches := re.FindStringSubmatch(alias)
		if len(matches) != 5 {
			continue
		}
		ip, port, method, password := matches[1], matches[2], matches[3], matches[4]
		cityCN, _ := item["city_cn"].(string)
		city, _ := item["city"].(string)
		out.WriteString(fmt.Sprintf("ss://%s:%s@%s:%s#%s %s\n", method, password, ip, port, cityCN, city))
	}

	if out.Len() == 0 {
		return "", fmt.Errorf("cnc07: no node extracted")
	}
	return out.String(), nil
}

func httpGet(ctx context.Context, client *http.Client, rawURL, ua string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", ua)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(body)), nil
}

func decodeBase64Loose(input string) ([]byte, error) {
	s := strings.TrimSpace(input)
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\r", "")
	if decoded, err := base64.StdEncoding.DecodeString(s); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.URLEncoding.DecodeString(s); err == nil {
		return decoded, nil
	}
	return base64.RawURLEncoding.DecodeString(s)
}

func aesDecryptCBCBase64(ciphertext, key, iv string) (string, error) {
	block, err := aes.NewCipher([]byte(key))
	if err != nil {
		return "", err
	}
	decoded, err := decodeBase64Loose(ciphertext)
	if err != nil {
		return "", err
	}
	if len(decoded) == 0 || len(decoded)%aes.BlockSize != 0 {
		return "", fmt.Errorf("invalid ciphertext length")
	}
	mode := cipher.NewCBCDecrypter(block, []byte(iv))
	mode.CryptBlocks(decoded, decoded)

	padding := int(decoded[len(decoded)-1])
	if padding <= 0 || padding > aes.BlockSize || padding > len(decoded) {
		return "", fmt.Errorf("invalid padding")
	}
	for i := len(decoded) - padding; i < len(decoded); i++ {
		if int(decoded[i]) != padding {
			return "", fmt.Errorf("invalid padding")
		}
	}
	return strings.TrimSpace(string(decoded[:len(decoded)-padding])), nil
}
