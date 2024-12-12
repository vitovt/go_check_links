package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"

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
}

// NewCrawler initializes a crawler with a given starting URL.
func NewCrawler(startURL string) (*Crawler, error) {
	u, err := url.Parse(startURL)
	if err != nil {
		return nil, err
	}

	return &Crawler{
		startURL: u,
		visited:  make(map[string]bool),
		client:   &http.Client{},
		results:  make(chan LinkStatus, 1000),
	}, nil
}

// Run starts the crawling process.
func (c *Crawler) Run(ctx context.Context) {
	c.wg.Add(1)
	go c.crawlURL(ctx, c.startURL)

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
	if c.visited[u] {
		return false
	}
	c.visited[u] = true
	return true
}

// crawlURL fetches the given URL, checks it, and if it is an HTML page, parses it for more links.
func (c *Crawler) crawlURL(ctx context.Context, u *url.URL) {
	defer c.wg.Done()

	if !c.shouldCrawl(u) {
		return
	}

	uStr := u.String()
	if !c.markVisited(uStr) {
		// Already visited
		return
	}

	resp, err := c.client.Get(uStr)
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
	if !strings.Contains(ct, "text/html") {
		return
	}

	doc, err := html.Parse(resp.Body)
	if err != nil {
		return
	}

	links := extractLinks(doc, u)
	for _, link := range links {
		c.wg.Add(1)
		go c.crawlURL(ctx, link)
	}
}

// shouldCrawl checks if the URL is within the same host and scheme.
func (c *Crawler) shouldCrawl(u *url.URL) bool {
	// Only follow same scheme/host
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
	if len(os.Args) < 2 {
		fmt.Printf("Usage: %s <start-url>\n", path.Base(os.Args[0]))
		os.Exit(1)
	}

	start := os.Args[1]
	ctx := context.Background()

	c, err := NewCrawler(start)
	if err != nil {
		log.Fatalf("Error initializing crawler: %v", err)
	}

	log.Printf("Starting crawl at: %s", start)
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
