package media

import (
	"crypto/md5"
	"errors"
	"fmt"
	"github.com/dustin/go-humanize"
	"html/template"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
)

/**
This file will download the media from a URL and save it to disk.
*/

import (
	"bytes"
	"github.com/rs/zerolog/log"
	"io"
	"os"
	"os/exec"
	"sync"
)

type Media struct {
	Id          string
	Name        string
	SizeInBytes int64
	HumanSize   string
}

type MediaResults struct {
	Medias []Media
}

// TODO: Use something better than this. It's too tedious to map
var fetchResponseTmpl = template.Must(template.ParseFiles("templates/media/response.html"))
var fetchIndexTmpl = template.Must(template.ParseFiles("templates/media/index.html"))

// Where the media files are saved. Always has a trailing slash
var downloadDir = getDownloadDir()
var idCharSet = regexp.MustCompile(`^[a-zA-Z0-9]+$`).MatchString

func Index(w http.ResponseWriter, _ *http.Request) {
	if err := fetchIndexTmpl.Execute(w, nil); err != nil {
		log.Error().Msgf("Error rendering template: %v", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
	}
}

func FetchMedia(w http.ResponseWriter, r *http.Request) {
	url := r.URL.Query().Get("url")
	if url == "" {
		http.Error(w, "Missing URL", http.StatusBadRequest)
		return
	}

	// NOTE: This system is for a simple use case, meant to run at home. This is not a great design for a robust system.
	// We are hashing the URL here and writing files to disk to a consistent directory based on the ID. You can imagine
	// concurrent users would break this for the same URL. That's fine given this is for a simple home system.
	// Future work can make this more sophisticated.
	id := GetMD5Hash(url)
	// Look to see if we already have the media on disk
	medias, err := getAllFilesForId(id)
	if len(medias) == 0 {
		// We don't, so go fetch it
		id, err = fetch(url)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		medias, err = getAllFilesForId(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	response := MediaResults{
		Medias: medias,
	}

	if err := fetchResponseTmpl.Execute(w, response); err != nil {
		log.Error().Msgf("Error rendering template: %v", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
	}
}

// returns the ID of the file
func fetch(url string) (string, error) {
	// The id will be used as the name of the parent directory of the output files
	id := GetMD5Hash(url)
	name := getMediaDirectory(id) + "%(title)s.%(ext)s"

	log.Info().Msgf("Downloading %s to %s", url, id)

	cmd := exec.Command("youtube-dl",
		"--format", "bestvideo+bestaudio[ext=m4a]/bestvideo+bestaudio/best",
		"--merge-output-format", "mp4",
		"--restrict-filenames",
		"--write-info-json",
		"--output", name,
		url)

	var stdoutBuf, stderrBuf bytes.Buffer
	stdoutIn, _ := cmd.StdoutPipe()
	stderrIn, _ := cmd.StderrPipe()

	var errStdout, errStderr error
	stdout := io.MultiWriter(os.Stdout, &stdoutBuf)
	stderr := io.MultiWriter(os.Stderr, &stderrBuf)

	err := cmd.Start()
	if err != nil {
		log.Error().Msgf("Error starting command: %v", err)
		return "", err
	}

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		_, errStdout = io.Copy(stdout, stdoutIn)
		wg.Done()
	}()

	_, errStderr = io.Copy(stderr, stderrIn)
	wg.Wait()
	log.Info().Msgf("Done with %s", id)

	err = cmd.Wait()
	if err != nil {
		log.Error().Msgf("cmd.Run() failed with %s", err)
		return "", err
	} else if errStdout != nil {
		log.Error().Msgf("failed to capture stdout: %v", errStdout)
	} else if errStderr != nil {
		log.Error().Msgf("failed to capture stderr: %v", errStderr)
	}

	return id, nil
}

// Returns the relative directory containing the media file, with a trailing slash
// Id is expected to be pre validated
func getMediaDirectory(id string) string {
	return downloadDir + id + "/"
}

// id is expected to be validated prior to calling this func
func getAllFilesForId(id string) ([]Media, error) {
	root := getMediaDirectory(id)
	file, err := os.Open(root)
	if err != nil {
		return nil, err
	}
	files, _ := file.Readdirnames(0) // 0 to read all files and folders
	if len(files) == 0 {
		return nil, errors.New("ID not found")
	}

	var medias []Media

	// We expect two files to be produced for each video, a json manifest and an mp4.
	for _, f := range files {
		if !strings.HasSuffix(f, ".json") {
			fi, err := os.Stat(root + f)
			var size int64 = 0
			if err == nil {
				size = fi.Size()
			}

			media := Media{
				Id:          id,
				Name:        filepath.Base(f),
				SizeInBytes: size,
				HumanSize:   humanize.Bytes(uint64(size)),
			}
			medias = append(medias, media)
		}
	}

	return medias, nil
}

// id is expected to be validated prior to calling this func
// TODO: This needs to handle multiple files in the directory
func getFileFromId(id string) (string, error) {
	root := getMediaDirectory(id)
	file, err := os.Open(root)
	if err != nil {
		return "", err
	}
	files, _ := file.Readdirnames(0) // 0 to read all files and folders
	if len(files) == 0 {
		return "", errors.New("ID not found")
	}

	// We expect two files to be produced, a json manifest and an mp4. We want to return the mp4
	// Sometimes the video file might not have an mp4 extension, so filter out the json file
	for _, f := range files {
		if !strings.HasSuffix(f, ".json") {
			// TODO: This is just returning the first file found. We need to handle multiple
			return root + f, nil
		}
	}

	return "", errors.New("unable to find file")
}

func GetMD5Hash(url string) string {
	return fmt.Sprintf("%x", md5.Sum([]byte(url)))
}

func isValidId(id string) bool {
	// TODO: Finish this. Should only be alpha numeric
	return idCharSet(id)
}

func getDownloadDir() string {
	dir := os.Getenv("MR_DOWNLOAD_DIR")
	if dir != "" {
		if !strings.HasSuffix(dir, "/") {
			return dir + "/"
		}
		return dir
	}
	return "downloads/"
}
