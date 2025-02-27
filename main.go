package main

import (
	"bytes"
	"crypto/tls"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig"
	"github.com/google/go-github/v34/github"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
)

func newRoundTripper(accessToken string, insecure bool) http.RoundTripper {
	// Reuse default transport that has timeouts and supports proxies
	transport := http.DefaultTransport.(*http.Transport)
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: insecure}
	return &roundTripper{accessToken, transport}
}

type roundTripper struct {
	accessToken string
	underlying  http.RoundTripper
}

func (rt roundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	r.Header.Set("Authorization", fmt.Sprintf("token %s", rt.accessToken))
	return rt.underlying.RoundTrip(r)
}

var (
	token              = flag.String("token", os.Getenv("GITHUB_TOKEN"), "Github access token")
	owner              = flag.String("owner", os.Getenv("GITHUB_OWNER"), "Github repository owner")
	repo               = flag.String("repo", os.Getenv("GITHUB_REPO"), "Github repository name")
	commentType        = flag.String("type", os.Getenv("GITHUB_COMMENT_TYPE"), "Comment type: 'commit', 'pr', 'issue', 'pr-review' or 'pr-file'")
	sha                = flag.String("sha", os.Getenv("GITHUB_COMMIT_SHA"), "Commit SHA")
	number             = flag.String("number", os.Getenv("GITHUB_PR_ISSUE_NUMBER"), "Pull Request or Issue number")
	file               = flag.String("file", os.Getenv("GITHUB_PR_FILE"), "Pull Request File Name")
	position           = flag.String("position", os.Getenv("GITHUB_PR_FILE_POSITION"), "Position in Pull Request File")
	templ              = flag.String("template", os.Getenv("GITHUB_COMMENT_TEMPLATE"), "Template to format comment. Supports `Go` templates: My comment:<br/>{{.}}. Use either `template` or `template_file`")
	templateFile       = flag.String("template_file", os.Getenv("GITHUB_COMMENT_TEMPLATE_FILE"), "The path to a template file to format comment. Supports `Go` templates. Use either `template` or `template_file`")
	format             = flag.String("format", os.Getenv("GITHUB_COMMENT_FORMAT"), "Alias of `template`")
	formatFile         = flag.String("format_file", os.Getenv("GITHUB_COMMENT_FORMAT_FILE"), "Alias of `template_file`")
	comment            = flag.String("comment", os.Getenv("GITHUB_COMMENT"), "Comment text")
	deleteCommentRegex = flag.String("delete-comment-regex", os.Getenv("GITHUB_DELETE_COMMENT_REGEX"), "Regex to find previous comments to delete before creating the new comment. Supported for comment types `commit`, `pr-file`, `issue` and `pr`")
	editCommentRegex   = flag.String("edit-comment-regex", os.Getenv("GITHUB_EDIT_COMMENT_REGEX"), "Regex to find previous comments to replace with new content, or create new comment if none found. Supported for comment types `commit`, `pr-file`, `issue` and `pr`")
	baseURL            = flag.String("baseURL", os.Getenv("GITHUB_BASE_URL"), "Base URL of github enterprise")
	uploadURL          = flag.String("uploadURL", os.Getenv("GITHUB_UPLOAD_URL"), "Upload URL of github enterprise")
	insecure           = flag.Bool("insecure", strings.ToLower(os.Getenv("GITHUB_INSECURE")) == "true", "Ignore SSL certificate check")
	useCommitShaforPR  = flag.Bool("use-sha-for-pr", strings.ToLower(os.Getenv("GITHUB_USE_SHA_FOR_PR")) == "true", "Use commit sha to find PR number")
	state              = flag.String("pr-state", os.Getenv("GITHUB_PR_STATE"), "State of the PR e.g closed,open. Default is open")
	baseBranch         = flag.String("base-branch", os.Getenv("GITHUB_PR_BASE_BRANCH"), "Base branch of pull request")
)

func getPullRequestOrIssueNumber(str string) (int, error) {
	if str == "" {
		return 0, errors.New("-number or GITHUB_PR_ISSUE_NUMBER required")
	}

	num, err := strconv.Atoi(str)
	if err != nil {
		return 0, errors.WithMessage(err, "-number or GITHUB_PR_ISSUE_NUMBER must be an integer")
	}

	return num, nil
}


func getPullRequestNumberFromSha( sha, state, base string, client *github.Client) (int, error) {

	pullRequestsService := client.PullRequests
	opts :=  &github.PullRequestListOptions {
		State: state,
		Base: base,
	}
	pullRequests,_,err :=  pullRequestsService.ListPullRequestsWithCommit(context.Background(), *owner, *repo, sha, opts, )
	if err !=nil {
		return 0, err
	}
	return *pullRequests[0].Number, nil
}

func getPullRequestFilePosition(str string) (int, error) {
	if str == "" {
		return 0, errors.New("-position or GITHUB_PR_FILE_POSITION required")
	}

	position, err := strconv.Atoi(str)
	if err != nil {
		return 0, errors.WithMessage(err, "-position or GITHUB_PR_FILE_POSITION must be an integer")
	}

	return position, nil
}

func getComment() (string, error) {
	// Read the comment from the command-line argument or ENV var first
	if *comment != "" {
		return *comment, nil
	}

	// Read from stdin
	data, err := ioutil.ReadAll(os.Stdin)
	if err != nil {
		return "", errors.WithMessage(err, "Comment must be provided either as command-line argument, ENV variable, or from 'stdin'")
	}

	return string(data), nil
}

func formatComment(comment string) (string, error) {
	if *format == "" && *formatFile == "" && *templ == "" && *templateFile == "" {
		return comment, nil
	}

	var t *template.Template
	var err error
	var templateFinal string
	var templateFileFinal string

	if *format != "" || *templ != "" {
		if *templ != "" {
			templateFinal = *templ
		} else {
			templateFinal = *format
		}
		t = template.New("formatComment").Funcs(sprig.TxtFuncMap())
		t, err = t.Parse(templateFinal)
		if err != nil {
			return "", err
		}
	} else {
		if *templateFile != "" {
			templateFileFinal = *templateFile
		} else {
			templateFileFinal = *formatFile
		}
		name := path.Base(templateFileFinal)
		t = template.New(name).Funcs(sprig.TxtFuncMap())
		t, err = t.ParseFiles(templateFileFinal)
		if err != nil {
			return "", err
		}
	}

	var doc bytes.Buffer

	err = t.Execute(&doc, comment)
	if err != nil {
		return "", err
	}

	// Remove ANSI escape codes
	const ansi = "[\u001B\u009B][[\\]()#;?]*(?:(?:(?:[a-zA-Z\\d]*(?:;[a-zA-Z\\d]*)*)?\u0007)|(?:(?:\\d{1,4}(?:;\\d{0,4})*)?[\\dA-PRZcf-ntqry=><~]))"
	var re = regexp.MustCompile(ansi)
	var s = doc.String()
	return re.ReplaceAllString(s, ""), nil
}

func main() {
	flag.Parse()

	if *token == "" {
		flag.PrintDefaults()
		log.Fatal("-token or GITHUB_TOKEN required")
	}
	if *owner == "" {
		flag.PrintDefaults()
		log.Fatal("-owner or GITHUB_OWNER required")
	}
	if *repo == "" {
		flag.PrintDefaults()
		log.Fatal("-repo or GITHUB_REPO required")
	}
	if *commentType == "" {
		flag.PrintDefaults()
		log.Fatal("-type or GITHUB_COMMENT_TYPE required")
	}
	if *commentType != "commit" && *commentType != "pr" && *commentType != "issue" && *commentType != "pr-review" && *commentType != "pr-file" {
		flag.PrintDefaults()
		log.Fatal("-type or GITHUB_COMMENT_TYPE must be one of 'commit', 'pr', 'issue', 'pr-review' or 'pr-file'")
	}

	http.DefaultClient.Transport = newRoundTripper(*token, *insecure)

	var githubClient *github.Client
	if *baseURL != "" || *uploadURL != "" {
		if *baseURL == "" {
			flag.PrintDefaults()
			log.Fatal("-baseURL or GITHUB_BASE_URL required when using -uploadURL or GITHUB_UPLOAD_URL")
		}
		if *uploadURL == "" {
			flag.PrintDefaults()
			log.Fatal("-uploadURL or GITHUB_UPLOAD_URL required when using -baseURL or GITHUB_BASE_URL")
		}
		githubClient, _ = github.NewEnterpriseClient(*baseURL, *uploadURL, http.DefaultClient)
	} else {
		githubClient = github.NewClient(http.DefaultClient)
	}

	// https://developer.github.com/v3/guides/working-with-comments
	// https://developer.github.com/v3/repos/comments
	if *commentType == "commit" {
		if *sha == "" {
			flag.PrintDefaults()
			log.Fatal("-sha or GITHUB_COMMIT_SHA required")
		}

		comment, err := getComment()
		if err != nil {
			log.Fatal(err)
		}

		formattedComment, err := formatComment(comment)
		if err != nil {
			log.Fatal(err)
		}
		commitComment := &github.RepositoryComment{Body: &formattedComment}

		// Find and delete existing comment(s) before creating the new one
		if *deleteCommentRegex != "" {
			r, err := regexp.Compile(*deleteCommentRegex)
			if err != nil {
				log.Fatal(err)
			}

			listOptions := &github.ListOptions{}
			comments, _, err := githubClient.Repositories.ListCommitComments(context.Background(), *owner, *repo, *sha, listOptions)
			if err != nil {
				log.Println("github-commenter: Error listing commit comments: ", err)
			} else {
				for _, comment := range comments {
					if r.MatchString(*comment.Body) {
						_, err = githubClient.Repositories.DeleteComment(context.Background(), *owner, *repo, *comment.ID)
						if err != nil {
							log.Println("github-commenter: Error deleting commit comment: ", err)
						} else {
							log.Println("github-commenter: Deleted commit comment: ", *comment.ID)
						}
					}
				}
			}
		}

		// Find and update existing comment with new content
		if *editCommentRegex != "" {
			found := false
			r, err := regexp.Compile(*editCommentRegex)
			if err != nil {
				log.Fatal(err)
			}

			listOptions := &github.ListOptions{}
			comments, _, err := githubClient.Repositories.ListCommitComments(context.Background(), *owner, *repo, *sha, listOptions)
			if err != nil {
				log.Println("github-commenter: Error listing commit comments: ", err)
			} else {
				for _, comment := range comments {
					if r.MatchString(*comment.Body) {
						found = true
						_, _, err = githubClient.Repositories.UpdateComment(context.Background(), *owner, *repo, *comment.ID, commitComment)
						if err != nil {
							log.Fatal("github-commenter: Error updating commit comment: ", err)
						} else {
							log.Println("github-commenter: Updated commit comment: ", *comment.ID)
						}
					}
				}
			}
			if found {
				return // exit
			}
		}

		commitComment, _, err = githubClient.Repositories.CreateComment(context.Background(), *owner, *repo, *sha, commitComment)
		if err != nil {
			log.Fatal(err)
		}

		log.Println("github-commenter: Created GitHub Commit comment", *commitComment.ID)
	} else if *commentType == "pr-review" {
		var prNumber int
		if *useCommitShaforPR  {
			if *baseBranch == ""  ||  *state == "" {
				flag.PrintDefaults()
				log.Fatal("github-commenter: ( -pr-state or GITHUB_PR_STATE ) and ( -basebranch or GITHUB_PR_BASE_BRANCH )  must be provided when using flag -use-sha-for-pr ")
			}
			num,err := getPullRequestNumberFromSha(*sha, *state, *baseBranch, githubClient)
			if err != nil{
				log.Fatal(err)
			}
			prNumber = num

		} else {
			// https://developer.github.com/v3/pulls/reviews/#create-a-pull-request-review
			num, err := getPullRequestOrIssueNumber(*number)
			if err != nil {
				log.Fatal(err)
			}
			prNumber = num
		}

		


		comment, err := getComment()
		if err != nil {
			log.Fatal(err)
		}

		formattedComment, err := formatComment(comment)
		if err != nil {
			log.Fatal(err)
		}

		pullRequestReviewRequest := &github.PullRequestReviewRequest{Body: &formattedComment, Event: github.String("COMMENT")}
		pullRequestReview, _, err := githubClient.PullRequests.CreateReview(context.Background(), *owner, *repo, prNumber, pullRequestReviewRequest)
		if err != nil {
			log.Fatal(err)
		}

		log.Println("github-commenter: Created GitHub PR Review comment", *pullRequestReview.ID)
	} else if *commentType == "issue" || *commentType == "pr" {
		// https://developer.github.com/v3/issues/comments
		num, err := getPullRequestOrIssueNumber(*number)
		if err != nil {
			log.Fatal(err)
		}

		comment, err := getComment()
		if err != nil {
			log.Fatal(err)
		}

		formattedComment, err := formatComment(comment)
		if err != nil {
			log.Fatal(err)
		}
		issueComment := &github.IssueComment{Body: &formattedComment}

		// Find and delete existing comment(s) before creating the new one
		if *deleteCommentRegex != "" {
			r, err := regexp.Compile(*deleteCommentRegex)
			if err != nil {
				log.Fatal(err)
			}

			listOptions := &github.IssueListCommentsOptions{}
			comments, _, err := githubClient.Issues.ListComments(context.Background(), *owner, *repo, num, listOptions)
			if err != nil {
				log.Println("github-commenter: Error listing Issue/PR comments: ", err)
			} else {
				for _, comment := range comments {
					if r.MatchString(*comment.Body) {
						_, err = githubClient.Issues.DeleteComment(context.Background(), *owner, *repo, *comment.ID)
						if err != nil {
							log.Println("github-commenter: Error deleting Issue/PR comment: ", err)
						} else {
							log.Println("github-commenter: Deleted Issue/PR comment: ", *comment.ID)
						}
					}
				}
			}
		}
		// Find and update existing comment(s) with new content
		if *editCommentRegex != "" {
			found := false
			r, err := regexp.Compile(*editCommentRegex)
			if err != nil {
				log.Fatal(err)
			}

			listOptions := &github.IssueListCommentsOptions{}
			comments, _, err := githubClient.Issues.ListComments(context.Background(), *owner, *repo, num, listOptions)
			if err != nil {
				log.Println("github-commenter: Error listing Issue/PR comments: ", err)
			} else {
				for _, comment := range comments {
					if r.MatchString(*comment.Body) {
						found = true
						_, _, err = githubClient.Issues.EditComment(context.Background(), *owner, *repo, *comment.ID, issueComment)
						if err != nil {
							log.Fatal("github-commenter: Error updating Issue/PR comment: ", err)
						} else {
							log.Println("github-commenter: Updated Issue/PR comment: ", *comment.ID)
						}
					}
				}
			}
			if found {
				return // exit
			}
		}

		issueComment, _, err = githubClient.Issues.CreateComment(context.Background(), *owner, *repo, num, issueComment)
		if err != nil {
			log.Fatal(err)
		}

		log.Println("github-commenter: Created GitHub Issue comment", *issueComment.ID)
	} else if *commentType == "pr-file" {
		// https://developer.github.com/v3/pulls/comments
		num, err := getPullRequestOrIssueNumber(*number)
		if err != nil {
			log.Fatal(err)
		}

		if *sha == "" {
			flag.PrintDefaults()
			log.Fatal("-sha or GITHUB_COMMIT_SHA required")
		}

		if *file == "" {
			flag.PrintDefaults()
			log.Fatal("-file or GITHUB_PR_FILE required")
		}

		position, err := getPullRequestFilePosition(*position)
		if err != nil {
			log.Fatal(err)
		}

		comment, err := getComment()
		if err != nil {
			log.Fatal(err)
		}

		formattedComment, err := formatComment(comment)
		if err != nil {
			log.Fatal(err)
		}
		pullRequestComment := &github.PullRequestComment{Body: &formattedComment, Path: file, Position: &position, CommitID: sha}

		// Find and delete existing comment(s) before creating the new one
		if *deleteCommentRegex != "" {
			r, err := regexp.Compile(*deleteCommentRegex)
			if err != nil {
				log.Fatal(err)
			}

			listOptions := &github.PullRequestListCommentsOptions{}
			comments, _, err := githubClient.PullRequests.ListComments(context.Background(), *owner, *repo, num, listOptions)
			if err != nil {
				log.Println("github-commenter: Error listing PR file comments: ", err)
			} else {
				for _, comment := range comments {
					if r.MatchString(*comment.Body) {
						_, err = githubClient.PullRequests.DeleteComment(context.Background(), *owner, *repo, *comment.ID)
						if err != nil {
							log.Println("github-commenter: Error deleting PR file comment: ", err)
						} else {
							log.Println("github-commenter: Deleted PR file comment: ", *comment.ID)
						}
					}
				}
			}
		}

		// Find and update existing comment with new content
		if *editCommentRegex != "" {
			found := false
			r, err := regexp.Compile(*editCommentRegex)
			if err != nil {
				log.Fatal(err)
			}

			// edit mode: create a new request with only the new comment body.
			// The API call will fail if the req includes other fields (path, commit_id, position)
			editComment := &github.PullRequestComment{Body: pullRequestComment.Body}

			listOptions := &github.PullRequestListCommentsOptions{}
			comments, _, err := githubClient.PullRequests.ListComments(context.Background(), *owner, *repo, num, listOptions)
			if err != nil {
				log.Println("github-commenter: Error listing PR file commit comments: ", err)
			} else {
				for _, comment := range comments {
					if r.MatchString(*comment.Body) {
						found = true
						_, _, err = githubClient.PullRequests.EditComment(context.Background(), *owner, *repo, *comment.ID, editComment)
						if err != nil {
							log.Fatal("github-commenter: Error updating PR file comment: ", err)
						} else {
							log.Println("github-commenter: Updated PR file comment: ", *comment.ID)
						}
					}
				}
			}
			if found {
				return // exit
			}
		}

		pullRequestComment, _, err = githubClient.PullRequests.CreateComment(context.Background(), *owner, *repo, num, pullRequestComment)
		if err != nil {
			log.Fatal(err)
		}

		log.Println("github-commenter: Created GitHub PR comment on file: ", *pullRequestComment.ID)
	}
}
