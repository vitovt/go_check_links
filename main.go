package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/spf13/pflag"
	"golang.org/x/net/html"
)

type LinkStatus struct {
	URL    string
	Status int
	Err    error
}

type Crawler struct {
	startURL    *url.URL
	visited     map[string]bool
	visitedLock sync.Mutex
	client      *http.Client
	results     chan LinkStatus
	wg          sync.WaitGroup
	userAgent   string

	delay        time.Duration
	debug        bool
	maxNum       int
	visitedCount int
}

var (
	// Command-line options
	ignoreCert bool
	delay      time.Duration
	timeout    time.Duration
	debug      bool
	maxNum     int
)

// NewCrawler initializes a crawler with a given starting URL.
func NewCrawler(startURL string, ignoreCert bool, delay time.Duration, timeout time.Duration, debug bool, maxNum int) (*Crawler, error) {
	u, err := url.Parse(startURL)
	if err != nil {
		return nil, err
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: ignoreCert},
	}

	client := &http.Client{
		Jar:       jar,
		Transport: transport,
		Timeout:   timeout,
	}

	return &Crawler{
		startURL:  u,
		visited:   make(map[string]bool),
		client:    client,
		results:   make(chan LinkStatus, 1000),
		userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/115.0.0.0 Safari/537.36",
		delay:     delay,
		debug:     debug,
		maxNum:    maxNum,
	}, nil
}

// Run starts the crawling process.
func (c *Crawler) Run(ctx context.Context) {
	c.wg.Add(1)
	go c.crawlURL(ctx, c.startURL, nil)
	// Close results channel once all work is done.
	go func() {
		c.wg.Wait()
		close(c.results)
	}()
}

// Wait waits for the crawl results to finish and returns them.
func (c *Crawler) Wait() []LinkStatus {
	var allResults []LinkStatus
	for r := range c.results {
		allResults = append(allResults, r)
	}
	return allResults
}

func (c *Crawler) markVisited(u string) bool {
	c.visitedLock.Lock()
	defer c.visitedLock.Unlock()

	// If maxNum > 0, limit the number of pages visited
	if c.maxNum > 0 && c.visitedCount >= c.maxNum {
		return false
	}

	if c.visited[u] {
		return false
	}
	c.visited[u] = true
	c.visitedCount++
	return true
}

// crawlURL fetches the given URL, checks it, and if it is an HTML page, parses it for more links.
func (c *Crawler) crawlURL(ctx context.Context, u *url.URL, referer *url.URL) {
	defer c.wg.Done()

	if !c.shouldCrawl(u) {
		return
	}

	uStr := u.String()
	if !c.markVisited(uStr) {
		// Already visited or max reached
		return
	}

	// If delay is set, sleep a random duration up to that delay
	if c.delay > 0 {
		time.Sleep(time.Duration(rand.Int63n(int64(c.delay))))
	}

	req, err := http.NewRequestWithContext(ctx, "GET", uStr, nil)
	if err != nil {
		c.results <- LinkStatus{URL: uStr, Err: err}
		return
	}

	// Set some "browser-like" headers
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,image/apng,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	if referer != nil {
		req.Header.Set("Referer", referer.String())
	}

	resp, err := c.client.Do(req)
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	c.results <- LinkStatus{URL: uStr, Status: status, Err: err}

	if err != nil {
		// Can't proceed if request failed
		return
	}
	defer resp.Body.Close()

	// Only parse HTML pages
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(strings.ToLower(ct), "text/html") {
		return
	}

	doc, err := html.Parse(resp.Body)
	if err != nil {
		return
	}

	// If debug enabled, print a debug message
	if c.debug {
		fmt.Printf("DEBUG: Retrieved HTML for %s (content output not fully implemented)\n", uStr)
	}

	links := extractLinks(doc, u)
	for _, link := range links {
		c.wg.Add(1)
		go c.crawlURL(ctx, link, u)
	}
}

// shouldCrawl checks if the URL is within the same host and scheme.
func (c *Crawler) shouldCrawl(u *url.URL) bool {
	if !strings.EqualFold(u.Host, c.startURL.Host) || u.Scheme != c.startURL.Scheme {
		return false
	}
	return true
}

// extractLinks finds all <a href=...> and <img src=...> links from the parsed HTML and returns absolute URLs.
func extractLinks(n *html.Node, base *url.URL) []*url.URL {
	var links []*url.URL
	var f func(*html.Node)
	f = func(node *html.Node) {
		if node.Type == html.ElementNode {
			var keyAttr string
			switch node.Data {
			case "a":
				keyAttr = "href"
			case "img":
				keyAttr = "src"
			}
			if keyAttr != "" {
				for _, attr := range node.Attr {
					if attr.Key == keyAttr {
						u, err := url.Parse(strings.TrimSpace(attr.Val))
						if err == nil {
							resolved := base.ResolveReference(u)
							links = append(links, resolved)
						}
					}
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			f(child)
		}
	}
	f(n)
	return links
}

func main() {
	pflag.BoolVarP(&ignoreCert, "ignore-cert", "i", false, "Ignore invalid (self-signed or expired) TLS certificates")
	pflag.DurationVarP(&delay, "delay", "d", 0, "Random delay up to this duration before each request (e.g. 2s, 500ms). Default: 0")
	pflag.DurationVarP(&timeout, "timeout", "t", 10*time.Second, "HTTP client timeout (e.g. 10s, 5s). Default: 10s")
	pflag.BoolVarP(&debug, "debug", "v", false, "Print retrieved HTML content info (for debugging)")
	pflag.IntVarP(&maxNum, "max-num", "m", 0, "Restrict the maximum number of pages to scan. Default: no limit")

	pflag.Usage = func() {
		progName := path.Base(os.Args[0])
		fmt.Printf("Usage: %s [options] <start-url>\n", progName)
		fmt.Println()
		fmt.Println("This program crawls a website starting from <start-url>, following links within the same host and scheme,")
		fmt.Println("and reports on broken links (HTTP errors).")
		fmt.Println()
		pflag.PrintDefaults()
		fmt.Println()
		fmt.Println("Example:")
		fmt.Printf("  %s --ignore-cert --delay=2s --timeout=5s --debug --max-num=100 https://example.com\n", progName)
	}

	pflag.Parse()

	if pflag.NArg() < 1 {
		pflag.Usage()
		os.Exit(1)
	}

	start := pflag.Arg(0)
	ctx := context.Background()

	c, err := NewCrawler(start, ignoreCert, delay, timeout, debug, maxNum)
	if err != nil {
		log.Fatalf("Error initializing crawler: %v", err)
	}

	log.Printf("Starting crawl at: %s (ignore cert: %v, delay: %v, timeout: %v, debug: %v, max-num: %d)",
		start, ignoreCert, delay, timeout, debug, maxNum)
	c.Run(ctx)
	results := c.Wait()

	log.Println("Crawl finished. Results:")
	var brokenLinks []LinkStatus
	for _, r := range results {
		if r.Err != nil || (r.Status >= 400 && r.Status < 600) {
			brokenLinks = append(brokenLinks, r)
		}
	}

	for _, r := range results {
		if r.Err != nil {
			log.Printf("[BROKEN] %s -> Error: %v", r.URL, r.Err)
		} else {
			if r.Status >= 400 && r.Status < 600 {
				log.Printf("[BROKEN] %s -> HTTP %d", r.URL, r.Status)
			} else {
				log.Printf("[OK] %s -> HTTP %d", r.URL, r.Status)
			}
		}
	}

	if len(brokenLinks) == 0 {
		log.Println("No broken links found!")
	} else {
		log.Printf("Found %d broken links:", len(brokenLinks))
		for _, b := range brokenLinks {
			if b.Err != nil {
				log.Printf(" - %s (%v)", b.URL, b.Err)
			} else {
				log.Printf(" - %s (Status: %d)", b.URL, b.Status)
			}
		}
	}
}

