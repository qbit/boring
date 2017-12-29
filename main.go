package main

import (
	"bufio"
	"flag"
	"fmt"
	"html/template"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	. "github.com/gorilla/feeds"
	"github.com/russross/blackfriday"
)

var templ *template.Template

var funcMap = template.FuncMap{
	"hasTitle": func(s string) bool {
		ret := false
		if s != "" {
			ret = true
		}
		return ret
	},
	"formatDate": func(t time.Time) string {
		return t.Format(time.RFC1123)
	},
	"shortDate": func(t time.Time) string {
		return t.Format("January _2, 2006")
	},
	"printByte": func(b []byte) string {
		return string(b)
	},
	"lop": func(p Posts, start, end int) Posts {
		if len(p) < end {
			return p
		}
		return p[start:end]
	},
	"joinTags": func(ts Tags) template.HTML {
		var s []string
		for _, t := range ts {
			s = append(s, fmt.Sprintf(`%s`, t.Name))
		}
		return template.HTML(strings.Join(s, ", "))
	},
	"printHTML": func(b []byte) template.HTML {
		return template.HTML(string(b))
	},
}

type content struct {
	Posts  Posts
	Title  string
	Author User
}

// AuthorRE is a regex to grab our Authors
var AuthorRE = regexp.MustCompile(`^author:\s(.*)$`)

// TitleRE matches our article title
var TitleRE = regexp.MustCompile(`^title:\s(.*)$`)

// DateRE matches our article date
var DateRE = regexp.MustCompile(`^date:\s(.*)$`)

// TagRE matches the tags for a given post
var TagRE = regexp.MustCompile(`^tags:\s(.*)$`)

// DescRE matches the descriptoin for a given post
var DescRE = regexp.MustCompile(`^description:\s(.*)$`)

// Tag represents a specific tag for an article
type Tag struct {
	ID      int
	Created time.Time
	Name    string
}

// Tags are a collection of Tag
type Tags []*Tag

// Join returns a concat'd string of Tag names
func (t *Tags) Join() []string {
	var s []string
	for _, t := range *t {
		s = append(s, t.Name)
	}
	return s
}

func (t *Tags) String() string {
	return strings.Join(t.Join(), ", ")
}

// User represents an author of an article
type User struct {
	LName  string
	FName  string
	Email  string
	Pubkey []byte
	User   string
}

var userLineRE = regexp.MustCompile(`^(.*)\s(.*)\s<(.*)>$`)

// Parse takes a 'First Last <user@email.com>' style string and creates a User
func (u *User) Parse(s string) {
	u.FName = userLineRE.ReplaceAllString(s, "$1")
	u.LName = userLineRE.ReplaceAllString(s, "$2")
	u.Email = userLineRE.ReplaceAllString(s, "$3")
}

// Combine concatenates FName, LName and Email into one line
func (u *User) Combine() string {
	return fmt.Sprintf("%s %s", u.FName, u.LName)
}

// Post is the base type for all posts
type Post struct {
	Title       string
	Description string
	Date        time.Time
	Body        []byte
	Author      User
	Signed      bool
	Signature   []byte
	Tags        Tags
	URL         string
}

// HTML returns converted MD to HTML
func (p *Post) HTML() {
	p.Body = blackfriday.MarkdownCommon(p.Body)
}

// LoadFromFile takes the File of a given page and loads the markdown for rendering
func (p *Post) LoadFromFile(f string) error {
	file, err := os.Open(f)
	if err != nil {
		return err
	}
	scanner := bufio.NewScanner(file)
	if err != nil {
		return err
	}

	for scanner.Scan() {
		var line = scanner.Bytes()
		useLine := true
		if AuthorRE.Match(line) {
			aline := AuthorRE.ReplaceAllString(string(line), "$1")
			p.Author.Parse(aline)
			fmt.Printf("Author: %s %s (%s)\n", p.Author.FName, p.Author.LName, p.Author.Email)
			useLine = false
		}
		if TitleRE.Match(line) {
			p.Title = TitleRE.ReplaceAllString(string(line), "$1")
			fmt.Printf("Title: %s\n", p.Title)
			useLine = false
		}
		if DateRE.Match(line) {
			d := DateRE.ReplaceAllString(string(line), "$1")
			p.Date, err = time.Parse(time.RFC1123, d)
			if err != nil {
				log.Printf("error in '%s'\n", f)
				log.Fatal(err)
			}
			fmt.Printf("Date: %s\n", p.Date)
			useLine = false
		}

		if TagRE.Match(line) {
			ts := TagRE.ReplaceAllString(string(line), "$1")
			for _, tag := range strings.Split(ts, ",") {
				var t Tag
				t.Name = strings.TrimSpace(tag)
				p.Tags = append(p.Tags, &t)
			}
			fmt.Printf("Tags: %s\n", p.Tags.Join())
			useLine = false
		}

		if DescRE.Match(line) {
			p.Description = DescRE.ReplaceAllString(string(line), "$1")
			fmt.Printf("Description: %s\n", p.Description)
			useLine = false
		}

		if useLine {
			p.Body = append(p.Body, line...)
			p.Body = append(p.Body, 10)
		}
	}

	if err != nil {
		return err
	}
	return nil
}

// Posts represent a collection of a set of Post
type Posts []*Post

func (p Posts) Len() int {
	return len(p)
}

func (p Posts) Less(i, j int) bool {
	return p[i].Date.After(p[j].Date)
}

func (p Posts) Swap(i, j int) {
	p[i], p[j] = p[j], p[i]
}

func renderPost(f string, path string) (Post, error) {
	var err error
	p := Post{}

	p.LoadFromFile(f)

	if err != nil {
		log.Fatal(err)
	}

	p.HTML()
	p.URL = "/" + md2html(path)

	return p, nil
}

func renderTemplate(dst string, tmpl string, data interface{}) {
	o, err := os.Create(dst)
	defer o.Close()

	if err != nil {
		log.Fatal(err)
	}

	err = templ.ExecuteTemplate(o, tmpl, data)
	if err != nil {
		log.Println(dst)
		log.Fatal(err)
	}
}

func md2html(f string) string {
	return strings.Replace(f, ".md", ".html", -1)
}

func main() {
	var err error
	// extrasys.Pledge("stdio wpath rpath cpath", nil)

	var watch = flag.Bool("w", false, "Enable 'watch' mode. Requires 'wdir' and 'wcmd'.")
	var watchDir = flag.String("wdir", "", "watch a directory for changes, run command when change happens.")
	var watchCmd = flag.String("wcmd", "", "command to run when changes are detected in 'wdir'.")
	var srvPort = flag.String("port", ":8080", "Port to serve the static files on.")

	flag.Parse()

	if !*watch {
		if len(os.Args) < 2 {
			fmt.Println("Wrong number of arguments")
			os.Exit(1)
		}

		src := os.Args[1]
		tmpl := os.Args[2]
		dst := os.Args[3]

		templ, err = template.New("boring").Funcs(funcMap).ParseGlob(tmpl + "/*.html")
		if err != nil {
			log.Fatal(err)
		}

		log.Printf("Generating static html from %s to %s\n", src, dst)

		files, err := ioutil.ReadDir(src)
		if err != nil {
			log.Fatal(err)
		}

		posts := Posts{}
		for _, file := range files {
			fn := file.Name()
			srcFile := path.Join(src, fn)
			dstFile := path.Join(dst, "/posts/", md2html(fn))
			post, err := renderPost(srcFile, path.Join("posts/", fn))
			fmt.Println("-----")
			if err != nil {
				log.Fatal(err)
			}

			renderTemplate(dstFile, "default.html", struct {
				Content Post
			}{
				post,
			})

			posts = append(posts, &post)
		}

		sort.Sort(posts)

		renderTemplate(path.Join(dst, "/index.html"), "index.html", content{
			Title: "",
			Posts: posts,
		})
		renderTemplate(path.Join(dst, "/about.html"), "about.html", content{
			Title:  "About",
			Author: posts[0].Author,
		})
		renderTemplate(path.Join(dst, "/contact.html"), "contact.html", content{
			Title:  "Contact",
			Author: posts[0].Author,
		})
		if len(posts) < 5 {
			renderTemplate(path.Join(dst, "/archive.html"), "archive.html", content{
				Title: "Archive",
				Posts: posts,
			})
		} else {
			renderTemplate(path.Join(dst, "/archive.html"), "archive.html", content{
				Title: "Archive",
				Posts: posts[5:],
			})
		}

		// TODO variablize all of this and shove it in some kind of config

		latestDate := posts[0].Date

		feed := &Feed{
			Title:       "deftly.net - All posts",
			Link:        &Link{Href: "https://deftly.net/"},
			Description: "Personal blog of Aaron Bieber",
			Author:      &Author{Name: "Aaron Bieber", Email: "aaron@bolddaemon.com"},
			Created:     latestDate,
			Copyright:   "This work is copyright © Aaron Bieber",
		}

		for _, post := range posts {
			var i = &Item{}
			i.Title = post.Title
			i.Description = string(post.Body)
			i.Link = &Link{Href: "https://deftly.net" + post.URL}
			i.Author = &Author{Name: post.Author.Combine(), Email: "aaron@bolddaemon.com"}
			i.Created = post.Date

			feed.Items = append(feed.Items, i)
		}

		atomFile, err := os.Create(path.Join(dst, "atom.xml"))
		if err != nil {
			log.Fatal(err)
		}

		rssFile, err := os.Create(path.Join(dst, "rss.xml"))
		if err != nil {
			log.Fatal(err)
		}

		feed.WriteAtom(atomFile)
		feed.WriteRss(rssFile)
	} else {
		// Watch mode

		go func() {
			// Start a http server and serve the static dir
			log.Printf("listening on https://localhost%s", *srvPort)
			log.Fatal(
				http.ListenAndServe(
					*srvPort,
					http.FileServer(http.Dir("static/")),
				),
			)
		}()

		watcher, err := fsnotify.NewWatcher()
		if err != nil {
			log.Fatal(err)
		}
		defer watcher.Close()

		done := make(chan bool)
		go func() {
			for {
				select {
				case event := <-watcher.Events:
					if event.Op&fsnotify.Write == fsnotify.Write {
						log.Println("modified file:", event.Name)
						c := exec.Command(*watchCmd)

						if err := c.Run(); err != nil {
							fmt.Println("Error: ", err)
						}
					}
				case err := <-watcher.Errors:
					log.Fatal(err)
				}
			}
		}()

		err = watcher.Add(*watchDir)
		if err != nil {
			log.Fatal(err)
		}
		<-done
	}
}
