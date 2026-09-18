const api = globalThis.browser || globalThis.chrome;
let step = 0;
let choice = 'both';
let locale = 'en';
let serviceReady = false;

const copy = {
  en:{brandSub:'Browser setup',step1:'01 · Local service',serviceTitle:'Connect this browser to ContextBridge.',serviceCopy:'The token authorizes only your local service. Page access stays separate and explicit.',tokenLabel:'Pairing token',show:'Show',hide:'Hide',notChecked:'Not checked yet',step2:'02 · Browser slots',tabsTitle:'Choose what ContextBridge may open.',tabsCopy:'Fresh chats avoid mixing jobs with personal conversations. You can attach an existing page yourself later.',bothTitle:'Fresh ChatGPT + Gemini chats',recommended:'Recommended first setup',laterTitle:'I will attach pages later',laterCopy:'Finish pairing without opening a site',step3:'03 · Ready',readyTitle:'Ready to connect.',readyCopy:'ContextBridge will request only the selected site permissions, create fresh chats when selected, and connect them to the local service.',service:'Service',slots:'Browser slots',policy:'Session policy',manual:'Manual · change later',testContinue:'Test & continue',continue:'Continue',connect:'Open & connect',finish:'Finish',paired:'Paired',connected:'Connected. You can close this tab.',noToken:'Enter the pairing token.',testFailed:'The local service did not accept this token.'},
  de:{brandSub:'Browser-Einrichtung',step1:'01 · Lokaler Dienst',serviceTitle:'Verbinde diesen Browser mit ContextBridge.',serviceCopy:'Das Token autorisiert nur deinen lokalen Dienst. Seitenzugriff bleibt getrennt und ausdrücklich.',tokenLabel:'Pairing-Token',show:'Anzeigen',hide:'Verbergen',notChecked:'Noch nicht geprüft',step2:'02 · Browser-Slots',tabsTitle:'Wähle, was ContextBridge öffnen darf.',tabsCopy:'Frische Chats verhindern eine Vermischung mit privaten Unterhaltungen. Bestehende Seiten kannst du später selbst anhängen.',bothTitle:'Frische ChatGPT- und Gemini-Chats',recommended:'Empfohlen für den ersten Start',laterTitle:'Ich hänge Seiten später an',laterCopy:'Kopplung ohne neue Website abschließen',step3:'03 · Bereit',readyTitle:'Bereit zum Verbinden.',readyCopy:'ContextBridge fragt nur die gewählten Website-Rechte an, erstellt auf Wunsch frische Chats und verbindet sie mit dem lokalen Dienst.',service:'Dienst',slots:'Browser-Slots',policy:'Sitzungsmodus',manual:'Manuell · später änderbar',testContinue:'Prüfen & weiter',continue:'Weiter',connect:'Öffnen & verbinden',finish:'Abschließen',paired:'Gekoppelt',connected:'Verbunden. Du kannst diesen Tab schließen.',noToken:'Gib das Pairing-Token ein.',testFailed:'Der lokale Dienst hat dieses Token nicht akzeptiert.'}
};
const $ = (id)=>document.getElementById(id);
const t = (key)=>copy[locale][key]||key;

document.addEventListener('DOMContentLoaded', async()=>{
  $('version').textContent=`v${api.runtime.getManifest().version}`;
  const saved=await api.storage.local.get({bridgeUrl:'http://127.0.0.1:32145',token:'',uiLocale:'en'});
  locale=saved.uiLocale==='de'?'de':'en'; $('bridge-url').value=saved.bridgeUrl; $('token').value=saved.token;
  render();
});

function render(){
  document.documentElement.lang=locale;
  document.querySelectorAll('[data-i18n]').forEach((node)=>{node.textContent=t(node.dataset.i18n);});
  $('brand-sub').textContent=t('brandSub'); $('language').textContent=locale==='en'?'DE':'EN';
  document.querySelectorAll('.step').forEach((node,index)=>node.classList.toggle('active',index===step));
  document.querySelectorAll('.progress i').forEach((node,index)=>node.classList.toggle('done',index<=step));
  $('back').disabled=step===0; $('next').querySelector('span').textContent=step===0?t('testContinue'):step===1?t('continue'):(choice==='none'?t('finish'):t('connect'));
  $('review-service').textContent=serviceReady?t('paired'):t('notChecked');
  $('review-slots').textContent=choice==='both'?'ChatGPT + Gemini':choice==='chatgpt'?'ChatGPT':choice==='gemini'?'Gemini':(locale==='de'?'Später':'Later');
}

$('language').addEventListener('click',async()=>{locale=locale==='en'?'de':'en';await api.storage.local.set({uiLocale:locale});render();});
$('show-token').addEventListener('click',()=>{const showing=$('token').type==='text';$('token').type=showing?'password':'text';$('show-token').textContent=t(showing?'show':'hide');});
document.querySelectorAll('.choice').forEach((button)=>button.addEventListener('click',()=>{choice=button.dataset.choice;document.querySelectorAll('.choice').forEach((node)=>{const selected=node===button;node.setAttribute('aria-checked',String(selected));node.querySelector('b').textContent=selected?'✓':'';});render();}));
$('back').addEventListener('click',()=>{if(step>0){step--;clearError();render();}});
$('next').addEventListener('click',async()=>{
  clearError(); setLoading(true);
  try{
    if(step===0){await testService();step=1;}
    else if(step===1){step=2;}
    else{await finish();$('next').querySelector('span').textContent=t('connected');$('next').disabled=true;$('back').disabled=true;return;}
    render();
  }catch(error){showError(error.message||String(error));}
  finally{setLoading(false);}
});

async function testService(){
  const bridgeUrl=$('bridge-url').value.trim().replace(/\/$/,''); const token=$('token').value.trim();
  if(!token)throw new Error(t('noToken'));
  const origin=permissionPattern(bridgeUrl);
  if(!await api.permissions.request({origins:[origin]}))throw new Error(t('testFailed'));
  await api.storage.local.set({bridgeUrl,token});
  const result=await api.runtime.sendMessage({type:'test'});
  if(!result?.ok)throw new Error(result?.error||t('testFailed'));
  serviceReady=true;$('service-state').classList.add('ok');$('service-state').querySelector('span').textContent=t('paired');
}

async function finish(){
  const providers=choice==='both'?['chatgpt','gemini']:choice==='none'?[]:[choice];
  const origins=providers.map((name)=>name==='chatgpt'?'https://chatgpt.com/*':'https://gemini.google.com/*');
  if(origins.length&&!await api.permissions.request({permissions:['tabs'],origins}))throw new Error(locale==='de'?'Website-Zugriff wurde nicht freigegeben.':'Website access was not granted.');
  const created=[];
  for(const provider of providers){
    const tab=await api.tabs.create({url:provider==='chatgpt'?'https://chatgpt.com/':'https://gemini.google.com/app',active:false});
    if(tab?.id)created.push(tab.id);
  }
  const saved=await api.storage.local.get({tabIds:[]});
  await api.storage.local.set({tabIds:[...new Set([...(saved.tabIds||[]),...created])],sessionMode:'manual',autoReconnect:true});
  await api.runtime.sendMessage({type:'refresh-tabs'});
  if(created.length){const result=await api.runtime.sendMessage({type:'start'});if(!result?.ok)throw new Error(result?.error||'Connection failed');}
}
function setLoading(value){$('next').classList.toggle('loading',value);$('next').disabled=value;$('back').disabled=value||step===0;}
function showError(message){$('error').textContent=message;$('error').classList.add('show');}
function clearError(){$('error').textContent='';$('error').classList.remove('show');}
function permissionPattern(value){
  const url=new URL(value);
  if(!['http:','https:'].includes(url.protocol))throw new Error('Only HTTP(S) services are supported.');
  return `${url.protocol}//${url.hostname}/*`;
}
