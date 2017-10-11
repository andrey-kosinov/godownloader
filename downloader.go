package main

import (
	"crypto/md5"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"time"
)

import (
	"github.com/cavaliercoder/grab"
	"github.com/go-ini/ini"
	"github.com/go-martini/martini"
	_ "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
	"github.com/martini-contrib/render"
)

type File struct {
	ID         uint
	Url        string
	Md5        string
	Tries      uint
	Ok         uint
	Progress   uint
	Bitrate    sql.NullString
	Resolution sql.NullString
	Created_at string
	Done_at    sql.NullString
	Error      sql.NullString
}

var db *sqlx.DB

func main() {

	/**
	 * Making log, config params
	 */

	f, err := os.OpenFile("downloader.log", os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		log.Fatal(err.Error())
	}
	defer f.Close()
	log.SetOutput(f)

	log.Print("Starting service...")

	cfg, err := ini.InsensitiveLoad("config.ini")
	if err != nil {
		fmt.Println(err.Error())
		log.Fatal("Fatal error: ", err.Error())
	}

	cfg_main, err := cfg.GetSection("main")
	if err != nil {
		fmt.Println("Config:", err.Error())
		log.Fatal("Fatal error in config: ", err.Error())
	}

	cfg_port, err := cfg_main.GetKey("port")
	if err != nil {
		fmt.Println("Config:", err.Error())
		log.Fatal("Fatal error in config: ", err.Error())
	}

	cfg_db, err := cfg.GetSection("db")
	if err != nil {
		fmt.Println("Config:", err.Error())
		log.Fatal("Fatal error in config: ", err.Error())
	}

	dbhost, err := cfg_db.GetKey("host")
	dbname, err := cfg_db.GetKey("database")
	dbuser, err := cfg_db.GetKey("user")
	dbpass, err := cfg_db.GetKey("password")
	if err != nil {
		fmt.Println("Config:", err.Error())
		log.Fatal("Fatal error in config: ", err.Error())
	}

	/**
	 * Connectng to MySQL
	 */

	constr := dbuser.String() + ":" + dbpass.String() + "@tcp(" + dbhost.String() + ")/" + dbname.String() + "?charset=utf8"
	db, err = sqlx.Open("mysql", constr)
	if err != nil {
		fmt.Println("Database:", err.Error())
		log.Fatal("Fatal error accessing database: ", err.Error())
	}
	defer db.Close()

	/**
	 * Getting list of unfinished file downloads and start goroutines for download them
	 * This is needed to recover after service fail
	 */

	files := []File{}
	err = db.Select(&files, "SELECT * FROM files")
	if err != nil {
		fmt.Println(err.Error())
		log.Fatal(err.Error())
	}

	for _, file := range files {
		if !file.Done_at.Valid {
			go download_file(file.ID, file.Url, file.Md5)
		}
	}

	/**
	 * Using Martini web framework to start and service http connections and return views
	 */

	log.Println("Using localhost:", cfg_port)

	m := martini.Classic()
	m.Logger(log.New(f, "[martini] ", log.LstdFlags))

	m.Use(render.Renderer(render.Options{
		Directory:  "views",
		Layout:     "layout",
		Extensions: []string{".tmpl", ".html"},
		Delims:     render.Delims{"{{", "}}"},
		Charset:    "UTF-8",
		IndentJSON: true}))

	m.Get("/", indexHandler)
	m.Get("/dl", downloadHandler)
	m.Get("/st", statusHandler)

	m.Get("/test", func() string {
		return "test!"
	})

	fmt.Printf("Service started at localhost:%v\n", cfg_port)
	m.RunOnAddr(":" + cfg_port.String())
	m.Run()

}

/**
 * Show home page with form on it to add new url for download
 * @param  {[type]} rnd render.Render Template views renderer
 * @param  {[type]} r   *http.Request Request object
 */
func indexHandler(rnd render.Render, r *http.Request) {
	rnd.HTML(http.StatusOK, "form", r)
}

/**
 * Add url and md5 for download. Gets parameter from URL: url and md5
 * @param  {[type]} rnd render.Render Template views renderer
 * @param  {[type]} r   *http.Request Request object
 */
func downloadHandler(rnd render.Render, r *http.Request) {

	url, _ := r.URL.Query()["url"]
	md5, _ := r.URL.Query()["md5"]

	file := File{}
	err := db.Get(&file, "SELECT * FROM files WHERE url=? OR md5=?", url[0], md5[0])
	if err != nil {
		log.Println(err.Error())
	}

	fmt.Printf("%+v\n", file)

	if file.ID > 0 {
		log.Println("URL or MD5 already added to the list")
		rnd.HTML(http.StatusOK, "duplicate", r)
		return
	}

	q, _ := db.Prepare("INSERT INTO files (url,md5) VALUES (?,?)")
	res, err := q.Exec(url[0], md5[0])
	if err != nil {
		fmt.Println(err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		fmt.Println(err)
	}

	go download_file(uint(id), url[0], md5[0])
	log.Printf("Added url (%v) with md5 (%v)", url, md5)

	rnd.HTML(http.StatusOK, "added", r)
}

/**
 * Show status of all downloads with current progress
 * @param  {[type]} rnd render.Render Template views renderer
 * @param  {[type]} r   *http.Request Request object
 */
func statusHandler(rnd render.Render, r *http.Request) {

	files := []File{}
	err := db.Select(&files, "SELECT * FROM files ORDER BY ok ASC, created_at DESC")
	if err != nil {
		fmt.Println(err.Error())
		log.Fatal(err.Error())
	}

	rnd.HTML(http.StatusOK, "status", files)
}

/**
 * Downloading file by url with progress indication and resuming partial downloaded files
 * @param  {[type]} id  uint          ID of url record in a database
 * @param  {[type]} url string        URL of file
 * @param  {[type]} md5 string        md5 summ for file
 */
func download_file(id uint, url string, md5 string) {

	file := File{}
	err := db.Get(&file, "SELECT * FROM files WHERE id=?", id)
	if err != nil {
		fmt.Println(err.Error())
		log.Fatal(err.Error())
	}

	client := grab.NewClient()
	req, _ := grab.NewRequest("./downloads", url)

	// start download
	log.Printf("Downloading %v...\n", req.URL())
	resp := client.Do(req)
	log.Printf("  %v\n", resp.HTTPResponse.Status)

	// start UI loop
	t := time.NewTicker(3000 * time.Millisecond)
	defer t.Stop()

Loop:
	for {
		select {
		case <-t.C:

			//fmt.Printf("%v \n", 100*resp.Progress())
			q, _ := db.Prepare("UPDATE files SET progress=? WHERE id=?")
			q.Exec(100*resp.Progress(), id)

			/*
				fmt.Printf("  transferred %v / %v bytes (%.2f%%)\n",
					resp.BytesComplete(),
					resp.Size,
					100*resp.Progress())
			*/
		case <-resp.Done:
			// download is complete
			q, _ := db.Prepare("UPDATE files SET progress=? WHERE id=?")
			q.Exec(100, id)

			break Loop
		}
	}

	// check for errors
	if err := resp.Err(); err != nil {

		log.Printf("Download failed: %v\n", err)
		q, _ := db.Prepare("UPDATE files SET progress=?, tries=tries+1, error=? WHERE id=?")
		q.Exec(0, err.Error(), id)

		if file.Tries < 3 {
			download_file(id, url, md5)
		} else {
			log.Println("Stop trying to download. Limit exceeded")
			q, _ := db.Prepare("UPDATE files SET done_at=NOW() WHERE id=?")
			q.Exec()
		}

	} else {
		filename := resp.Filename
		log.Printf("Download saved to ./%v \n", resp.Filename)

		hash, _ := ComputeMd5(filename)

		//fmt.Printf("%16x \n", hash)

		if fmt.Sprintf("%16x", hash) == md5 {
			log.Printf("OK. md5sums are equal for %v.\n", filename)

			bitrate, resolution, _ := VideoFileProbe(filename)

			q, _ := db.Prepare("UPDATE files SET ok=1, done_at=NOW(), error='', bitrate=?, resolution=? WHERE id=?")
			_, err := q.Exec(bitrate, resolution, id)
			if err != nil {
				fmt.Println(err)
			}

		} else {
			log.Printf("Warning. md5sums are NOT equal for %v.\n", filename)
			log.Printf("Computed: %16x , given: %v.\n", hash, md5)
			q, _ := db.Prepare("UPDATE files SET progress=?, tries=tries+1, error=? WHERE id=?")
			q.Exec(0, "md5sum mismatch", id)

			os.Remove(filename)

			if file.Tries < 3 {
				download_file(id, url, md5)
			} else {
				log.Println("Stop trying to download. Limit exceeded")
				q, _ := db.Prepare("UPDATE files SET done_at=NOW() WHERE id=?")
				q.Exec(id)
			}
		}

	}

}

/**
 * Compute MD5 summ for file even very big file
 * @param {[type]} filePath string	Path to file to compute md5
 */
func ComputeMd5(filePath string) ([]byte, error) {

	var result []byte
	file, err := os.Open(filePath)
	if err != nil {
		return result, err
	}
	defer file.Close()

	hash := md5.New()
	if _, err := io.Copy(hash, file); err != nil {
		return result, err
	}

	return hash.Sum(result), nil
}

/**
 * Wrapper to ffprobe that returns only bitrate and resolution
 * @param {[type]} filename string  File path to get data from
 */
func VideoFileProbe(filename string) (string, string, error) {

	type ProbeStream struct {
		CodecName string `json:"codec_name"`
		Width     uint   `json:"width,int"`
		Height    uint   `json:"height,int"`
	}

	type ProbeFormat struct {
		Filename         string            `json:"filename"`
		NBStreams        int               `json:"nb_streams"`
		NBPrograms       int               `json:"nb_programs"`
		FormatName       string            `json:"format_name"`
		FormatLongName   string            `json:"format_long_name"`
		StartTimeSeconds float64           `json:"start_time,string"`
		DurationSeconds  float64           `json:"duration,string"`
		Size             uint64            `json:"size,string"`
		BitRate          uint64            `json:"bit_rate,string"`
		ProbeScore       float64           `json:"probe_score"`
		Tags             map[string]string `json:"tags"`
	}

	type ProbeData struct {
		Streams []*ProbeStream `json:"streams,omitempty"`
		Format  *ProbeFormat   `json:"format,omitempty"`
	}

	cmd := exec.Command("ffprobe", "-show_format", "-show_streams", filename, "-print_format", "json")
	//cmd.Stderr = os.Stderr

	r, err := cmd.StdoutPipe()
	if err != nil {
		return "", "", err
	}

	//fmt.Printf("%v", r)

	err = cmd.Start()
	if err != nil {
		return "", "", err
	}

	var v ProbeData
	err = json.NewDecoder(r).Decode(&v)
	if err != nil {
		fmt.Println(err)
		return "", "", err
	}

	err = cmd.Wait()
	if err != nil {
		return "", "", err
	}

	return fmt.Sprintf("%v", v.Format.BitRate), fmt.Sprintf("%vx%v", v.Streams[0].Width, v.Streams[0].Height), nil
}
