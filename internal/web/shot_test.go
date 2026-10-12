package web

import (
	"os"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// TestScreenshot captures the live server (HIMAY_SHOT_URL) after optional clicks. Dev aid only.
func TestScreenshot(t *testing.T) {
	u := os.Getenv("HIMAY_SHOT_URL")
	if u == "" {
		t.Skip("dev aid")
	}
	actx, c1 := chromedp.NewExecAllocator(t.Context(), append(chromedp.DefaultExecAllocatorOptions[:], chromedp.WindowSize(shotWidth(), 900))...)
	defer c1()
	ctx, c2 := chromedp.NewContext(actx)
	defer c2()
	steps := []chromedp.Action[chromedp.Void]{chromedp.Navigate(u), chromedp.Sleep(800 * time.Millisecond)}
	if th := os.Getenv("HIMAY_SHOT_THEME"); th != "" {
		steps = append(steps, chromedp.Click(`[data-theme-set="`+th+`"]`, chromedp.NodeVisible), chromedp.Sleep(200*time.Millisecond))
	}
	if sel := os.Getenv("HIMAY_SHOT_CLICK"); sel != "" {
		steps = append(steps, chromedp.Evaluate[chromedp.Void](`document.querySelector(`+strconvQuote(sel)+`).scrollIntoView({block:"center"})`))
	}
	if sel := os.Getenv("HIMAY_SHOT_CLICK"); sel != "" {
		steps = append(steps, chromedp.Click(sel, chromedp.NodeVisible), chromedp.Sleep(800*time.Millisecond))
	}
	if k := os.Getenv("HIMAY_SHOT_KEYS"); k != "" {
		steps = append(steps, chromedp.KeyEvent(k), chromedp.Sleep(800*time.Millisecond))
	}
	if err := chromedp.Do(ctx, steps...); err != nil {
		t.Fatal(err)
	}
	png, err := chromedp.Run(ctx, chromedp.CaptureScreenshot())
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(os.Getenv("HIMAY_SHOT_OUT"), png, 0o644)
}

func shotWidth() int {
	if w := os.Getenv("HIMAY_SHOT_W"); w != "" {
		n := 0
		for _, c := range w {
			n = n*10 + int(c-'0')
		}
		return n
	}
	return 1600
}
