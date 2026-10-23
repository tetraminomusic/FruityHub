package app

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// SpeedTest — скорость связи с сервером FruityHub: одно соединение и четыре сразу. Если четыре заметно
// быстрее одного — провайдер (или что-то по дороге) режет каждое соединение по отдельности.
func SpeedTest(cfg *Config, job *Job) (string, error) {
	h := cfg.Hub()
	if !h.Registered() {
		return "", fmt.Errorf("сначала зарегистрируйся")
	}
	down := func(mb int, d time.Duration) (int64, time.Duration, error) {
		req, _ := http.NewRequestWithContext(job.Ctx(), "GET", fmt.Sprintf("%s/api/speedtest?mb=%d", cfg.ServerURL(), mb), nil)
		req.Header.Set("Authorization", "Bearer "+cfg.DeviceToken)
		t0 := time.Now()
		resp, err := streamClient().Do(req)
		if err != nil {
			return 0, 0, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return 0, 0, fmt.Errorf("сервер ответил %s — обнови сервер (./deploy/server-up.sh)", resp.Status)
		}
		var n int64
		buf := make([]byte, 1<<16)
		for time.Since(t0) < d {
			k, err := resp.Body.Read(buf)
			n += int64(k)
			if err != nil {
				break
			}
		}
		return n, time.Since(t0), nil
	}
	up := func(mb int) (int64, time.Duration, error) {
		data := bytes.Repeat([]byte{0x5a, 0xc3, 0x17, 0x99}, mb<<18)
		req, _ := http.NewRequestWithContext(job.Ctx(), "PUT", cfg.ServerURL()+"/api/speedtest", bytes.NewReader(data))
		req.Header.Set("Authorization", "Bearer "+cfg.DeviceToken)
		t0 := time.Now()
		resp, err := streamClient().Do(req)
		if err != nil {
			return 0, 0, err
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return int64(len(data)), time.Since(t0), nil
	}
	mbps := func(n int64, d time.Duration) float64 { return float64(n) / 1e6 / max(d.Seconds(), 0.001) }

	job.SetProgress("Скачивание с сервера, одно соединение…", 10)
	n1, d1, err := down(16, 12*time.Second)
	if err != nil {
		return "", err
	}
	job.SetProgress("Скачивание с сервера, четыре соединения сразу…", 40)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var n4 int64
	t4 := time.Now()
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, _, _ := down(8, 12*time.Second)
			mu.Lock()
			n4 += n
			mu.Unlock()
		}()
	}
	wg.Wait()
	d4 := time.Since(t4)
	job.SetProgress("Отправка на сервер…", 75)
	nu, du, uerr := up(4)

	s1, s4 := mbps(n1, d1), mbps(n4, d4)
	var b strings.Builder
	fmt.Fprintf(&b, "Сервер: %s\n\n", cfg.ServerURL())
	fmt.Fprintf(&b, "Скачивание, одно соединение: %.2f МБ/с\n", s1)
	fmt.Fprintf(&b, "Скачивание, четыре соединения: %.2f МБ/с\n", s4)
	if uerr == nil {
		fmt.Fprintf(&b, "Отправка: %.2f МБ/с\n", mbps(nu, du))
	} else {
		fmt.Fprintf(&b, "Отправка: не получилось (%v)\n", uerr)
	}
	b.WriteString("\n")
	switch {
	case s1 < 0.5 && s4 > s1*2.5:
		b.WriteString("Одно соединение режется, а несколько вместе идут быстрее — похоже, провайдер (или фильтрация трафика) ограничивает каждое соединение до зарубежного сервера. Помогает: передача по локальной сети (если вы рядом), VPN на компьютере или сервер FruityHub в своей стране.")
	case s1 < 0.5:
		b.WriteString("Связь с сервером медленная и с несколькими соединениями тоже. Проверь интернет (speedtest.net): если он быстрый — ограничен путь до сервера (провайдер, фильтрация, загрузка сервера). Помогает передача по локальной сети, VPN или сервер FruityHub в своей стране.")
	case s1 < 2:
		b.WriteString("Скорость средняя. Плагины и аудио будут идти, но не быстро.")
	default:
		b.WriteString("Связь с сервером хорошая. Если передача всё равно медленная — дело у второго участника: пусть тоже проверит скорость.")
	}
	DevLog("info", 0, false, "проверка скорости: 1 соединение %.2f МБ/с, 4 соединения %.2f МБ/с", s1, s4)
	return b.String(), nil
}
