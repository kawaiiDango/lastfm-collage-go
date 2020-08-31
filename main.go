package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"io/ioutil"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/chai2010/webp"
	"github.com/gocolly/colly/v2"
	"github.com/golang/freetype"
	"github.com/golang/freetype/truetype"
	"github.com/tidwall/gjson"
)

//collageType
const (
	ALBUM  = "album"
	ARTIST = "artist"
	TRACK  = "track"
)

var (
	font            *truetype.Font
	periods         = []string{"7day", "1month", "3month", "6month", "12month", "overall"}
	internalPeriods = map[string]string{periods[0]: "LAST_7_DAYS", periods[1]: "LAST_30_DAYS", periods[2]: "LAST_90_DAYS", periods[3]: "LAST_180_DAYS", periods[4]: "LAST_365_DAYS", periods[5]: "ALL"}
	collectorGlobal = colly.NewCollector(
		colly.UserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:80.0) Gecko/20100101 Firefox/80.0"),
		colly.AllowURLRevisit(),
		colly.IgnoreRobotsTxt(),
	)
	httpClient = &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	//prevent animated gif redirects as they may be too large. gif.Decode() reads the entire stream even if it returns the first frame
)

type tile struct {
	artist    string
	album     string
	track     string
	playCount int
	imgURL    string
	img       *image.Image
}

func init() {
	fontBytes, _ := ioutil.ReadFile("./NotoSansCJKtc-Medium.ttf")
	_font, err := freetype.ParseFont(fontBytes)
	font = _font
	if err != nil {
		fmt.Println(err)
		panic("font")
	}
}

func main() {
	http.HandleFunc("/collage", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm() //works for both get and post
		username := strings.TrimSpace(r.FormValue("username"))
		rows, _ := strconv.Atoi(r.FormValue("rows"))
		cols, _ := strconv.Atoi(r.FormValue("cols"))
		period := r.FormValue("period")
		collageType := r.FormValue("type")
		info, _ := strconv.ParseBool(r.FormValue("info"))
		playCount, _ := strconv.ParseBool(r.FormValue("playcount"))
		isWebp, _ := strconv.ParseBool(r.FormValue("webp"))
		var rgba *image.RGBA
		if username != "" &&
			rows > 0 && rows < 12 &&
			cols > 0 && cols < 12 &&
			inArray(period, &periods) &&
			(collageType == ALBUM || collageType == ARTIST || collageType == TRACK) {

			tiles := fetchTileData(username, rows, cols, period, collageType)
			if tiles == nil {
				rgba = drawError("Error fetching info or invalid username")
			} else {
				fetchImages(tiles)
				rgba = drawCollage(tiles, collageType, rows, cols, info, playCount)
			}

		} else {
			rgba = drawError("Missing or invaild parameters")
		}
		if isWebp {
			w.Header().Set("Content-Type", "image/webp")
			bytes, _ := webp.EncodeRGB(rgba, 90) //DefaulQuality = 90
			w.Write(bytes)
		} else {
			w.Header().Set("Content-Type", "image/jpeg")
			jpeg.Encode(w, rgba, &jpeg.Options{Quality: 85}) //DefaulQuality = 75
		}

	})

	fs := http.FileServer(http.Dir("./static/"))
	http.Handle("/", fs)
	fmt.Println("lastfmCollage listening on " + strconv.Itoa(PORT))
	http.ListenAndServe("127.0.0.1:"+strconv.Itoa(PORT), nil)
}

func fetchTileData(username string, rows int, cols int, period string, collageType string) *[]tile {
	tiles := make([]tile, 0, rows*cols)
	limit := rows*cols + 15
	collyFailed := false
	if collageType == ALBUM {
		parameters := url.Values{}
		parameters.Add("method", "user.gettopalbums")
		parameters.Add("format", "json")
		parameters.Add("api_key", LastfmAPIKey)
		parameters.Add("user", username)
		parameters.Add("period", period)
		parameters.Add("limit", strconv.Itoa(limit))
		query := parameters.Encode()

		resp, err := httpClient.Get("https://ws.audioscrobbler.com/2.0/?" + query)
		if err != nil {
			fmt.Println(err)
			return nil
		}
		if resp.Body != nil {
			defer resp.Body.Close()
		}
		if resp.StatusCode != 200 {
			return nil
		}
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			fmt.Println(err)
			return nil
		}
		albums := gjson.ParseBytes(body).Get("topalbums.album")
		albums.ForEach(func(key, album gjson.Result) bool {
			imgURL := album.Get("image.3.#text").String()
			if imgURL != "" {
				tiles = append(tiles, tile{
					artist:    album.Get("artist.name").String(),
					album:     album.Get("name").String(),
					playCount: int(album.Get("playcount").Int()),
					imgURL:    toWebp(imgURL),
				})
			}
			return len(tiles) < rows*cols // keep iterating
		})

	} else if collageType == ARTIST || collageType == TRACK {
		total := -1
		collector := collectorGlobal.Clone()
		collector.OnHTML("p.metadata-display", func(e *colly.HTMLElement) {
			total = parseHumanNumber(e.Text)
		})
		collector.OnHTML("tr.chartlist-row", func(e *colly.HTMLElement) {
			var track string
			var artist string
			var imgURL string
			if collageType == ARTIST {
				artist = e.ChildText(".chartlist-name > a")
				imgURL = e.ChildAttrs(".chartlist-image > .avatar > img", "src")[0]
				imgURL = strings.Replace(imgURL, "avatar70s", "avatar300s", 1)
			} else {
				artist = e.ChildText(".chartlist-artist > a")
				track = e.ChildText(".chartlist-name > a")
				imgURL = e.ChildAttrs(".chartlist-image > .cover-art > img", "src")[0]
				imgURL = strings.Replace(imgURL, "64s", "300x300", 1)
			}
			if strings.Index(imgURL, "2a96cbd8b46e442fc41c2b86b821562f") != -1 {
				return
			}

			playCountText := e.ChildText(".chartlist-count-bar-value")
			playCount, _ := strconv.Atoi(strings.Split(playCountText, " ")[0])

			tiles = append(tiles, tile{
				artist:    artist,
				track:     track,
				playCount: playCount,
				imgURL:    toWebp(imgURL),
			})
		})
		collector.OnError(func(r *colly.Response, err error) {
			fmt.Println("Request URL:", r.Request.URL, "failed with error:", err)
			collyFailed = true
		})

		for i := 0; i < min(3, int(math.Ceil(float64(limit)/float64(50)))); i++ {
			if collyFailed || total != -1 && total < (i+1)*50 {
				break
			}
			url := "https://www.last.fm/user/" + username + "/library/" + collageType + "s?date_preset=" + internalPeriods[period] + "&page=" + strconv.Itoa(i+1)
			collector.Visit(url)
		}
	}
	if collyFailed {
		return nil
	}
	return &tiles
}

func fetchImages(tiles *[]tile) {
	var wg sync.WaitGroup
	for i := 0; i < len(*tiles); i++ {
		wg.Add(1)
		go fetchOneImage(&(*tiles)[i], &wg)
	}
	wg.Wait()
}

func fetchOneImage(tile *tile, wg *sync.WaitGroup) {
	defer wg.Done()
	resp, _ := httpClient.Get(tile.imgURL)
	if resp.Body != nil {
		defer resp.Body.Close()
	}
	if resp.StatusCode == 200 {
		img, err := webp.Decode(resp.Body)
		if err == nil {
			tile.img = &img
		} else {
			fmt.Println(err, tile.imgURL)
		}
	} else {
		fmt.Println("failed to fetch img url:", tile.imgURL, "status:", resp.StatusCode)
	}
}

func drawCollage(tiles *[]tile, collageType string, rows int, cols int, info bool, playCount bool) *image.RGBA {
	dim := 300
	x := 0
	y := 0
	rgba := image.NewRGBA(image.Rect(0, 0, dim*cols, dim*rows))
	fgColor := color.RGBA{255, 255, 255, 255}
	shadowColor := color.RGBA{0, 0, 0, 255}
	fontSize := 18.0
	ctx := freetype.NewContext()
	ctx.SetFont(font)
	ctx.SetFontSize(fontSize)
	ctx.SetDst(rgba)
	for i, tile := range *tiles {
		if tile.img != nil {
			draw.Draw(rgba, image.Rect(x, y, x+dim, y+dim), *tile.img, image.Point{0, 0}, draw.Src)
		}
		if info || playCount {

			lines := make([]string, 0, 3)
			if info {
				if collageType == ALBUM {
					lines = append(lines, tile.album)
				} else if collageType == TRACK {
					lines = append(lines, tile.track)
				}
				lines = append(lines, tile.artist)
			}
			if playCount {
				lines = append(lines, strconv.Itoa(tile.playCount)+" plays")
			}
			ctx.SetClip(image.Rect(x, y, x+dim, y+dim))
			drawTextWithStroke(ctx, x+2, y+2, fontSize, &shadowColor, &fgColor, &lines)
		}

		if (i+1)%cols == 0 {
			x = 0
			y += dim
		} else {
			x += dim
		}
	}
	return rgba
}

func drawError(text string) *image.RGBA {
	rgba := image.NewRGBA(image.Rect(0, 0, 640, 65))
	fgColor := color.RGBA{255, 255, 255, 255}
	fontSize := 30.0

	ctx := freetype.NewContext()
	ctx.SetFont(font)
	ctx.SetFontSize(fontSize)
	ctx.SetClip(rgba.Bounds())
	ctx.SetSrc(image.NewUniform(fgColor))
	ctx.SetDst(rgba)
	pt := freetype.Pt(10, 10+int(ctx.PointToFixed(fontSize)>>6))
	ctx.DrawString(text, pt)
	return rgba
}

func drawTextWithStroke(ctx *freetype.Context, x int, y int, fontSize float64, shadowColor *color.RGBA, fgColor *color.RGBA, lines *[]string) {
	h := int(ctx.PointToFixed(fontSize) >> 6)
	hspace := 4
	stroke := 2
	for i, line := range *lines {
		xx := x + 10
		yy := y + (hspace+h)*(i+1)
		ctx.SetSrc(image.NewUniform(shadowColor))
		for j := -stroke; j <= stroke; j++ {
			for k := -stroke; k <= stroke; k++ {
				if !(j == 0 || k == 0) {
					pt := freetype.Pt(xx+j, yy+k)
					ctx.DrawString(line, pt)
				}
			}
		}
		ctx.SetSrc(image.NewUniform(fgColor))
		pt := freetype.Pt(xx, yy)
		ctx.DrawString(line, pt)
	}
}

func toWebp(url string) string {
	newURL := strings.Replace(url, ".jpg", ".webp", 1)
	newURL = strings.Replace(newURL, ".png", ".webp", 1)
	newURL = strings.Replace(newURL, ".gif", ".webp", 1)
	return newURL
}

func parseHumanNumber(text string) int {
	num := 0
	for _, c := range text {
		if c >= '0' && c <= '9' {
			num = num*10 + int(c-'0')
		}
	}
	return num
}

func min(x, y int) int {
	if x > y {
		return y
	}
	return x
}

func inArray(key string, arr *[]string) bool {
	for _, elem := range *arr {
		if elem == key {
			return true
		}
	}
	return false
}
