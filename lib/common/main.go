package common

import "fmt"

func (body ScrobbleBody) String() string {
	var title string
	if body.Movie != nil {
		title = fmt.Sprintf("%s (%d)", *body.Movie.Title, *body.Movie.Year)
	} else if body.Show != nil {
		title = fmt.Sprintf("%s - S%02dE%02d", *body.Show.Title, *body.Episode.Season, *body.Episode.Number)
	}
	return fmt.Sprintf("%s %d%%", title, body.Progress)
}
