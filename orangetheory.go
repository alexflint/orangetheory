package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"sort"

	"github.com/alexflint/go-arg"
	"github.com/alexflint/go-restructure"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

// regular expression pattern for email snippets from orangetheory
//
// example:
//
//   STUDIO WORKOUT SUMMARY Bothell, WA 06/13/2021 12‌:15 PM Tiffany 15 0 0 0 0 MINUTES / ZONE 55 CALORIES BURNED 0 SPLAT POINTS 75 AVG. HEART-RATE Peak HR: 80
type snippet struct {
	_                string `regexp:"STUDIO WORKOUT SUMMARY "`
	City             string `regexp:"\\w+"`
	_                string `regexp:", "`
	State            string `regexp:"\\w+"`
	_                string `regexp:" "`
	Month            string `regexp:"\\d+"`
	_                string `regexp:"/"`
	Day              string `regexp:"\\d+"`
	_                string `regexp:"/"`
	Year             string `regexp:"\\d+"`
	_                string `regexp:" "`
	Hour             string `regexp:"\\d+"`
	_                string `regexp:"\u200c:"` // not sure what this \u200c character is
	Minute           string `regexp:"\\d+"`
	_                string `regexp:" "`
	AMPM             string `regexp:"\\w{2}"`
	_                string `regexp:" "`
	Instructor       string `regexp:"\\w+"`
	_                string `regexp:" "`
	Zone1            string `regexp:"\\d+"`
	_                string `regexp:" "`
	Zone2            string `regexp:"\\d+"`
	_                string `regexp:" "`
	Zone3            string `regexp:"\\d+"`
	_                string `regexp:" "`
	Zone4            string `regexp:"\\d+"`
	_                string `regexp:" "`
	Zone5            string `regexp:"\\d+"`
	_                string `regexp:" MINUTES / ZONE "`
	Calories         string `regexp:"\\d+"`
	_                string `regexp:" CALORIES BURNED "`
	SplatPoints      string `regexp:"\\d+"`
	_                string `regexp:" SPLAT POINTS "`
	AverageHeartRate string `regexp:"\\d+"`
	_                string `regexp:" AVG. HEART-RATE Peak HR: "`
	PeakHeartRate    string `regexp:"\\d+"`
	//
}

// compile a regular expression for the struct above
var snippetParser = restructure.MustCompile(snippet{}, restructure.Options{})

// Retrieve a token, saves the token, then returns the generated client.
func getClient(config *oauth2.Config) *http.Client {
	// The file token.json stores the user's access and refresh tokens, and is
	// created automatically when the authorization flow completes for the first
	// time.
	tokFile := "token.json"
	tok, err := tokenFromFile(tokFile)
	if err != nil {
		tok = getTokenFromWeb(config)
		saveToken(tokFile, tok)
	}
	return config.Client(context.Background(), tok)
}

// Request a token from the web, then returns the retrieved token.
func getTokenFromWeb(config *oauth2.Config) *oauth2.Token {
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
	fmt.Printf("Go to the following link in your browser then type the "+
		"authorization code: \n%v\n", authURL)

	var authCode string
	if _, err := fmt.Scan(&authCode); err != nil {
		log.Fatalf("Unable to read authorization code: %v", err)
	}

	tok, err := config.Exchange(context.TODO(), authCode)
	if err != nil {
		log.Fatalf("Unable to retrieve token from web: %v", err)
	}
	return tok
}

// Retrieves a token from a local file.
func tokenFromFile(file string) (*oauth2.Token, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tok := &oauth2.Token{}
	err = json.NewDecoder(f).Decode(tok)
	return tok, err
}

// Saves a token to a file path.
func saveToken(path string, token *oauth2.Token) {
	fmt.Printf("Saving credential file to: %s\n", path)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		log.Fatalf("Unable to cache oauth token: %v", err)
	}
	defer f.Close()
	json.NewEncoder(f).Encode(token)
}

func main() {
	ctx := context.Background()

	var args struct {
		From   string `help:"Sender of Orange Theory data emails"`
		Output string `arg:"-o"`
	}
	args.From = "OTbeatReport@orangetheoryfitness.com"
	arg.MustParse(&args)

	// load oauth configuration
	b, err := ioutil.ReadFile("oauth.json")
	if err != nil {
		log.Fatalf("error reading client secret file: %v", err)
	}

	config, err := google.ConfigFromJSON(b, gmail.GmailReadonlyScope)
	if err != nil {
		log.Fatalf("error parsing client secret file to config: %v", err)
	}
	client := getClient(config)

	gm, err := gmail.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		log.Fatalf("error retrieving Gmail client: %v", err)
	}

	// search for emails from orangetheory
	messages, err := gm.Users.Messages.List("me").Q("from:" + args.From).Context(ctx).Do()
	if err != nil {
		log.Fatalf("error searching for emails: %v", err)
	}

	// parse the emails one-by-one (TODO: parallelize?)
	var snippets []snippet
	for _, msg := range messages.Messages {
		m, err := gm.Users.Messages.Get("me", msg.Id).Context(ctx).Do()
		if err != nil {
			log.Fatalf("error fetching email with id %q': %v", msg.Id, err)
		}

		var snippet snippet
		matched := snippetParser.Find(&snippet, m.Snippet)
		if !matched {
			fmt.Printf("snippet did not match pattern, ignoring: %s\n", m.Snippet)
			continue
		}

		snippets = append(snippets, snippet)
	}

	// sort by date
	sort.Slice(snippets, func(i, j int) bool {
		si := snippets[i]
		sj := snippets[j]
		di := fmt.Sprintf("%s-%s-%s", si.Year, si.Month, si.Day)
		dj := fmt.Sprintf("%s-%s-%s", sj.Year, sj.Month, sj.Day)
		return di < dj
	})

	// open output file
	var out io.Writer = os.Stdout
	if args.Output != "" {
		f, err := os.Open(args.Output)
		if err != nil {
			log.Fatal("error opening output file:", err)
		}
		defer f.Close()
		out = f
	}

	// write the CSV
	w := csv.NewWriter(out)
	defer w.Flush()

	w.Write([]string{
		"Date",
		"Time",
		"Zone 1",
		"Zone 2",
		"Zone 3",
		"Zone 4",
		"Zone 5",
		"Calories",
		"Average Heart Rate",
		"Peak Heart Rate",
		"Location",
	})

	for _, s := range snippets {
		w.Write([]string{
			fmt.Sprintf("%s/%s/%s", s.Month, s.Day, s.Year),
			fmt.Sprintf("%s:%s", s.Hour, s.Minute),
			s.Zone1,
			s.Zone2,
			s.Zone3,
			s.Zone4,
			s.Zone5,
			s.Calories,
			s.AverageHeartRate,
			s.PeakHeartRate,
			fmt.Sprintf("%s, %s", s.City, s.State),
		})
	}
}
