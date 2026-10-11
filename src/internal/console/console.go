// Package console is the scenario console: a small web page for running the
// demo. Pick a room and set it on fire (or start cooking or dust), clear the
// scenario, watch the detector's state, and act as the operator. It is a
// separate process from the simulator on purpose: physics-sim keeps one job
// (ground truth) behind a machine API, and the console is only a front end.
//
// It forwards a fixed list of requests and nothing else, so it cannot be used
// as an open proxy into the simulator or the detector.
package console

import (
	"html/template"
	"io"
	"net/http"
	"time"
)

// Links are shown at the top of the page; empty ones are left out.
type Links struct {
	Viewer  string
	Grafana string
}

// Handler serves the page and forwards the console's requests to the
// simulator's control API (physicsURL) and the detector's operator API
// (detectorURL).
func Handler(physicsURL, detectorURL string, links Links) http.Handler {
	client := &http.Client{Timeout: 5 * time.Second}
	forward := func(base, path string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			target := base + path
			if r.PathValue("level") != "" { // /detector/operator/{level}/{room}/{action}
				target = base + "/operator/" + r.PathValue("level") + "/" + r.PathValue("room") + "/" + r.PathValue("action")
			}
			req, err := http.NewRequestWithContext(r.Context(), r.Method, target, io.LimitReader(r.Body, 4<<10))
			if err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if ct := r.Header.Get("Content-Type"); ct != "" {
				req.Header.Set("Content-Type", ct)
			}
			resp, err := client.Do(req)
			if err != nil {
				http.Error(w, "upstream unreachable", http.StatusBadGateway)
				return
			}
			defer resp.Body.Close()
			if ct := resp.Header.Get("Content-Type"); ct != "" {
				w.Header().Set("Content-Type", ct)
			}
			w.WriteHeader(resp.StatusCode)
			_, _ = io.Copy(w, resp.Body)
		}
	}

	mux := http.NewServeMux()
	// the simulator
	mux.HandleFunc("POST /physics/fire", forward(physicsURL, "/control/fire"))
	mux.HandleFunc("POST /physics/benign", forward(physicsURL, "/control/benign"))
	mux.HandleFunc("POST /physics/clear", forward(physicsURL, "/control/clear"))
	mux.HandleFunc("GET /physics/state", forward(physicsURL, "/control/state"))
	// the detector
	mux.HandleFunc("GET /detector/status", forward(detectorURL, "/status"))
	mux.HandleFunc("POST /detector/operator/{level}/{room}/{action}", forward(detectorURL, ""))
	// the page
	page := template.Must(template.New("page").Parse(pageHTML))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = page.Execute(w, links)
	})
	return mux
}

const pageHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Scenario console</title>
<style>
 :root{--bg:#fff;--fg:#1a1a1a;--mut:#666;--line:#ddd;--ok:#16a34a;--warn:#ca8a04;--bad:#dc2626;--btn:#f3f4f6}
 @media (prefers-color-scheme:dark){:root{--bg:#111418;--fg:#e8e8e8;--mut:#9aa0a6;--line:#2c3138;--btn:#1f242b}}
 body{font:15px/1.45 system-ui,sans-serif;background:var(--bg);color:var(--fg);margin:0;padding:16px;max-width:980px;margin-inline:auto}
 h1{font-size:20px;margin:0 0 4px} h2{font-size:15px;margin:22px 0 8px}
 .mut{color:var(--mut)} a{color:inherit}
 .row{display:flex;gap:8px;flex-wrap:wrap;align-items:center}
 select,button{font:inherit;padding:7px 12px;border:1px solid var(--line);border-radius:6px;background:var(--btn);color:var(--fg);cursor:pointer}
 button.fire{border-color:var(--bad)} button:hover{filter:brightness(.95)}
 table{border-collapse:collapse;width:100%} td,th{text-align:left;padding:6px 8px;border-bottom:1px solid var(--line)}
 .pill{display:inline-block;padding:1px 9px;border-radius:99px;font-size:13px;color:#fff}
 .idle{background:var(--ok)} .warning,.cancelled{background:var(--warn)} .fire{background:var(--bad)}
 #msg{min-height:1.4em}
</style></head><body>
<h1>Scenario console</h1>
<div class="mut">Sim time <b id="sim">-</b> s
 {{if .Viewer}} &middot; <a href="{{.Viewer}}" target="_blank" rel="noopener">3D viewer</a>{{end}}
 {{if .Grafana}} &middot; <a href="{{.Grafana}}" target="_blank" rel="noopener">Grafana</a>{{end}}</div>

<h2>Start a scenario</h2>
<div class="row">
 <label>Room <select id="room"></select></label>
 <button class="fire" onclick="fire('slow')">Fire: slow</button>
 <button class="fire" onclick="fire('medium')">Fire: medium</button>
 <button class="fire" onclick="fire('fast')">Fire: fast</button>
 <button onclick="benign('cooking')">Cooking</button>
 <button onclick="benign('dust')">Dust</button>
 <button onclick="clearAll()">Clear scenarios</button>
</div>
<div id="msg" class="mut"></div>

<h2>Active sources</h2>
<table><thead><tr><th>Kind</th><th>Room</th><th>Started (sim s)</th><th>Remaining (s)</th></tr></thead><tbody id="active"></tbody></table>

<h2>Detector, per room</h2>
<table><thead><tr><th>Room</th><th>State</th><th>Tier</th><th>Operator</th></tr></thead><tbody id="rooms"></tbody></table>

<script>
const $=id=>document.getElementById(id);
function say(t){$('msg').textContent=t}
async function call(method,path,body){
  const r=await fetch(path,{method,headers:{'Content-Type':'application/json'},body:body?JSON.stringify(body):undefined});
  if(!r.ok) throw new Error(path+' -> '+r.status+' '+(await r.text()).trim());
  return r;
}
async function fire(growth){ try{ await call('POST','/physics/fire',{room:$('room').value,growth}); say('Fire ('+growth+') started in '+$('room').value); refresh() }catch(e){say(e.message)} }
async function benign(kind){ try{ await call('POST','/physics/benign',{kind,room:$('room').value,duration:600}); say(kind+' started in '+$('room').value+' for 600 sim s'); refresh() }catch(e){say(e.message)} }
async function clearAll(){ try{ await call('POST','/physics/clear'); say('All sources cleared; smoke and heat decay on their own'); refresh() }catch(e){say(e.message)} }
async function operator(room,action){ try{ await call('POST','/detector/operator/'+room+'/'+action,{}); say('Operator: '+action+' '+room); refresh() }catch(e){say(e.message)} }
function cell(tr,text){const td=document.createElement('td');td.textContent=text;tr.appendChild(td);return td}
async function refresh(){
  try{
    const st=await (await call('GET','/physics/state')).json();
    $('sim').textContent=Math.round(st.sim_time);
    const sel=$('room'); if(!sel.options.length){ st.rooms.forEach(r=>sel.add(new Option(r,r))) }
    const a=$('active'); a.replaceChildren();
    if(!st.active.length){ const tr=a.insertRow(); cell(tr,'none').colSpan=4 }
    st.active.forEach(s=>{const tr=a.insertRow();cell(tr,s.kind);cell(tr,s.room);cell(tr,Math.round(s.started_at));cell(tr,s.remaining?Math.round(s.remaining):'until cleared')});
  }catch(e){ $('sim').textContent='simulator unreachable' }
  try{
    const rooms=await (await call('GET','/detector/status')).json();
    const t=$('rooms'); t.replaceChildren();
    if(!rooms.length){ const tr=t.insertRow(); cell(tr,'no readings yet').colSpan=4 }
    rooms.forEach(r=>{
      const tr=t.insertRow(); cell(tr,r.Room);
      const s=cell(tr,''); const p=document.createElement('span'); p.className='pill '+r.State; p.textContent=r.State; s.appendChild(p);
      cell(tr,r.Tier); const op=cell(tr,'');
      [['confirm','Confirm fire'],['cancel','Cancel'],['release','Release']].forEach(([act,label])=>{
        const b=document.createElement('button'); b.textContent=label; b.onclick=()=>operator(r.Room,act); op.appendChild(b)});
    });
  }catch(e){}
}
refresh(); setInterval(refresh,2000);
</script></body></html>`
