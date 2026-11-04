package web

import "testing"

// TestMenubarOrderSettingsListsOMPAccounts runs the settings-page code in node:
// the menubar provider order lists one omp:<id> row per broker account, labels
// account rows "<tab name> · <account>" (a tab rename is only the prefix),
// keeps Codex labels, and maps a saved bare "omp" key onto every account row.
func TestMenubarOrderSettingsListsOMPAccounts(t *testing.T) {
	source, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	script := `
const assert=require('node:assert/strict');
const registry={};
function makeEl(id){ const el={id,classList:{_set:new Set(),add(c){this._set.add(c);},remove(c){this._set.delete(c);},toggle(){},contains(c){return this._set.has(c);}},setAttribute(){},removeAttribute(){},style:{},children:[],innerHTML:'',appendChild(){},querySelector:()=>null,querySelectorAll:()=>[],addEventListener(){}}; if(id) registry[id]=el; return el; }
const document={querySelector:()=>null,querySelectorAll:()=>[],getElementById:id=>registry[id]||null,createElement:()=>makeEl(null),addEventListener:()=>{},documentElement:{getAttribute:()=>null}};
const window={addEventListener:()=>{},location:{},matchMedia:()=>({matches:false,addEventListener:()=>{}})};
const localStorage={getItem:()=>null,setItem:()=>{}}; const sessionStorage=localStorage; const navigator={};
const getComputedStyle=()=>({getPropertyValue:()=>''});
function Chart(){} Chart.register=()=>{};
` + string(source) + `
(async()=>{
 const status={key:'omp',name:'OMP Broker',configured:true,pollingEnabled:true,dashboardVisible:true};
 const accounts=[{accountId:3,label:'Codex · me@example.com'},{accountId:4,label:'Cursor · work@example.com'}];

 // Rows and keys.
 assert.deepEqual(buildMenubarOMPItems([],status).map(i=>i.key),['omp'],'no accounts yet: the bare key stands in');
 const items=buildMenubarOMPItems(accounts,status);
 assert.deepEqual(items.map(i=>i.key),['omp:3','omp:4']);
 assert.equal(menubarOrderDisplayName(items[1],{}),'OMP · Cursor · work@example.com');

 // A renamed tab is only the prefix of account rows.
 assert.equal(menubarOrderDisplayName(items[1],{omp:'Broker'}),'Broker · Cursor · work@example.com');
 assert.equal(menubarOrderDisplayName(items[0],{omp:'Broker'}),'Broker · Codex · me@example.com');
 // Provider rows and single-profile Codex still take the whole rename.
 assert.equal(menubarOrderDisplayName({key:'anthropic',name:'Anthropic'},{anthropic:'Claude'}),'Claude');
 assert.equal(menubarOrderDisplayName({key:'codex:1',name:'Codex'},{codex:'Work'}),'Work');
 assert.equal(menubarOrderDisplayName({key:'codex:1',name:'Codex'},{}),'Codex');

 // fetchMenubarProviders through the real endpoints' shapes.
 fetch=async(url)=>{
  url=String(url);
  const json=(body)=>({ok:true,status:200,json:async()=>body});
  if(url.endsWith('/api/providers/status')) return json({providers:[{key:'anthropic',name:'Anthropic',pollingEnabled:true,dashboardVisible:true},status,{key:'codex',name:'Codex',pollingEnabled:true,dashboardVisible:true}]});
  if(url.includes('/api/current?provider=omp')) return json({provider:'omp',accounts});
  if(url.endsWith('/api/codex/profiles')) return json({profiles:[{id:1,name:'personal'},{id:2,name:'work <team>'}]});
  return {ok:false,status:404,json:async()=>({})};
 };
 const providers=await fetchMenubarProviders();
 assert.deepEqual(providers.map(p=>p.key),['anthropic','omp:3','omp:4','codex:1','codex:2']);
 const codexWork=providers.find(p=>p.key==='codex:2');
 assert.equal(menubarOrderDisplayName(codexWork,{}),'Codex - work <team>','profile names are escaped once, at render');
 assert.equal(menubarOrderDisplayName(codexWork,{codex:'Work'}),'Work - work <team>');

 // The settings list: a saved bare "omp" ranks and shows every account row.
 const list=makeEl('menubar-provider-order');
 State.menubarProviderOrder=['omp','anthropic'];
 State.menubarVisibleProviders=['omp'];
 State.dashboardProviderLabels={omp:'Broker'};
 await populateMenubarProviderOrder();
 const rows=[...list.innerHTML.matchAll(/data-provider="([^"]+)"/g)].map(m=>m[1]).filter((v,i,a)=>a.indexOf(v)===i);
 assert.deepEqual(rows.slice(0,3),['omp:3','omp:4','anthropic']);
 const row=key=>list.innerHTML.split('<li').find(chunk=>chunk.includes('data-provider="'+key+'"'));
 assert(!row('omp:3').includes('is-hidden'),'omp:3 shown via the bare key');
 assert(row('anthropic').includes('is-hidden'),'anthropic hidden');
 assert(row('omp:4').includes('Broker · Cursor · work@example.com'),'renamed tab prefixes the account');
})().catch(e=>{console.error(e);process.exit(1);});
`
	runNodeScript(t, script)
}
