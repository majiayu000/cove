"""Native isolated performance evidence, synthetic loopback upstream only."""
import argparse
import concurrent.futures
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import platform
import socket
import sqlite3
import statistics
import subprocess
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.request import Request, build_opener, ProxyHandler

ROOT = Path(__file__).resolve().parent.parent
HTTP = build_opener(ProxyHandler({}))

def percentile(values, p=.95):
    return sorted(values)[min(len(values)-1, int(len(values)*p))]

def free_port():
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0)); return s.getsockname()[1]

class Upstream(BaseHTTPRequestHandler):
    duration = 1800
    def log_message(self, *_): pass
    def do_POST(self):
        raw = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        if self.headers.get('Authorization') != 'Bearer performance-synthetic-secret':
            self.send_error(401); return
        self.send_response(200); self.send_header('Content-Type', 'text/event-stream'); self.end_headers()
        def emit(event, payload):
            payload['type'] = event
            self.wfile.write(('event: '+event+'\ndata: '+json.dumps(payload)+'\n\n').encode()); self.wfile.flush()
        try:
            emit('response.created', {'response': {'id':'perf-'+str(time.monotonic_ns()),'status':'in_progress'}})
            emit('response.output_text.delta', {'delta':'x','sent_ns':time.monotonic_ns()})
            if raw.get('input') in ('soak','cancel'):
                deadline=time.monotonic()+self.duration
                while time.monotonic()<deadline:
                    time.sleep(.1);emit('response.output_text.delta', {'delta':'x'*32})
            emit('response.completed', {'response':{'status':'completed','output':[], 'usage':{'input_tokens':50,'output_tokens':100}}})
        except (BrokenPipeError, ConnectionResetError): pass

def main():
    parser=argparse.ArgumentParser()
    parser.add_argument('--binary', default=str(ROOT/'bin/gatt'))
    parser.add_argument('--duration',type=int,default=1800)
    parser.add_argument('--rows',type=int,default=1000000)
    parser.add_argument('--output',default=str(ROOT/'test-results/performance-v12.json'))
    args=parser.parse_args()
    result={'synthetic_only':True,'os':platform.platform(),'arch':platform.machine(),'cpu':platform.processor(),'logical_cpus':os.cpu_count(),'duration_seconds':args.duration,'workers':8,'metadata_rows':args.rows}
    result['ram_bytes']=int(subprocess.check_output(['sysctl','-n','hw.memsize'],text=True).strip()) if platform.system()=='Darwin' else os.sysconf('SC_PAGE_SIZE')*os.sysconf('SC_PHYS_PAGES')
    Upstream.duration=args.duration
    upstream=ThreadingHTTPServer(('127.0.0.1',0),Upstream);upstream.daemon_threads=True
    threading.Thread(target=upstream.serve_forever,daemon=True).start()
    with tempfile.TemporaryDirectory(prefix='cove-performance-') as temp:
        temp=Path(temp);data=temp/'data';config=temp/'config.json'
        cfg=json.loads((ROOT/'config.example.json').read_text());cfg.update(listen='127.0.0.1:'+str(free_port()),data_dir=str(data),max_concurrent=8,total_timeout_seconds=args.duration+60)
        config.write_text(json.dumps(cfg));base='http://'+cfg['listen'];provider='http://127.0.0.1:'+str(upstream.server_port)
        process=None
        def launch():
            proc=subprocess.Popen([args.binary,'-config',str(config),'-background','serve'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
            for _ in range(100):
                if proc.poll() is not None: raise AssertionError('isolated process exited')
                try:
                    with HTTP.open(base+'/readyz',timeout=1) as response:
                        if response.status==200: return proc
                except Exception: pass
                time.sleep(.05)
            raise AssertionError('isolated startup timed out')
        def admin(path,method='GET',body=None,session=None):
            req=Request(base+'/admin/'+path,data=json.dumps(body).encode() if body is not None else None,method=method,headers={'Origin':base,'Content-Type':'application/json',**({'Authorization':'Bearer '+session} if session else {})})
            with HTTP.open(req,timeout=30) as resp: return json.load(resp)
        try:
            process=launch();session=admin('session','POST',{})['session_token']
            result['build_id']=admin('status',session=session)['build_id']
            account=admin('accounts','POST',{'provider':'openai_compatible','auth_type':'api_key','name':'Synthetic performance'},session)
            admin('accounts/'+account['id']+'/credential','POST',{'version':account['version'],'secret':'performance-synthetic-secret'},session)
            source=admin('sources','POST',{'account_id':account['id'],'name':'Synthetic source','base_url':provider,'models':['synthetic-model']},session)
            key=admin('client-keys','POST',{'source_id':source['id'],'name':'Synthetic client'},session)['secret']
            def stream(target,credential,kind):
                began=time.monotonic();first=None;count=0
                req=Request(target+'/v1/responses',data=json.dumps({'model':'synthetic-model','input':kind,'stream':True}).encode(),headers={'Content-Type':'application/json','Authorization':'Bearer '+credential})
                with HTTP.open(req,timeout=args.duration+60) as response:
                    for line in response:
                        count+=len(line)
                        if first is None and b'response.output_text.delta' in line and line.startswith(b'data:'):
                            first=(time.monotonic()-began)*1000
                        if kind=='cancel' and first is not None: break
                return {'first_content_ms':first,'bytes':count}
            paired=[]
            for _ in range(100):
                direct=stream(provider,'performance-synthetic-secret','ttft');gateway=stream(base,key,'ttft')
                paired.append(gateway['first_content_ms']-direct['first_content_ms'])
            result['additional_ttft_p95_ms']=percentile(paired)
            rss=[];began=time.monotonic()
            with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
                futures=[pool.submit(stream,base,key,'soak') for _ in range(8)]
                while not all(f.done() for f in futures):
                    rss.append({'elapsed_seconds':round(time.monotonic()-began,2),'rss_kib':int(subprocess.check_output(['ps','-o','rss=','-p',str(process.pid)],text=True).strip())})
                    time.sleep(5)
                result['streams']=[f.result() for f in futures]
            started=time.monotonic();stream(base,key,'cancel')
            while admin('status',session=session)['active_requests']!=0:
                if time.monotonic()-started>5: raise AssertionError('cancelled request did not release within five seconds')
                time.sleep(.05)
            result['cancel_release_seconds']=time.monotonic()-started
            time.sleep(5);result['rss_after_kib']=int(subprocess.check_output(['ps','-o','rss=','-p',str(process.pid)],text=True).strip());result['rss_samples']=rss
            result['rss_growth_kib']=max(v['rss_kib'] for v in rss)-min(v['rss_kib'] for v in rss)
            process.terminate();process.wait(timeout=10);process=None
            with sqlite3.connect(data/'gatt.db') as db:
                now=time.time();payload={'source_id':source['id'],'client_key_id':'synthetic-key','requested_model':'synthetic-model','protocol':'responses','status':'succeeded','origin':'client','usage':{'input_tokens':10,'output_tokens':2},'duration_ms':5,'observation_status':'complete','upstream_status':'completed','delivery_status':'completed'}
                def rows():
                    for i in range(args.rows):
                        identity=f'perf-row-{i:08}'
                        started=datetime.fromtimestamp(now-1-i*.01,timezone.utc).strftime('%Y-%m-%dT%H:%M:%S.%fZ')
                        yield identity,source['id'],started,'succeeded',json.dumps({**payload,'id':identity,'started_at':started,'ended_at':started})
                db.executemany('INSERT INTO requests(id,source_id,started,status,data) VALUES(?,?,?,?,?)',rows())
                result['seed_seconds']=time.time()-now
            process=launch();session=admin('session','POST',{})['session_token'];timings=[]
            for _ in range(30):
                started=time.monotonic();page=admin('requests?limit=50',session=session);timings.append((time.monotonic()-started)*1000)
                assert len(page['items'])<=50
            result['request_first_page_p95_ms']=percentile(timings)
            result['checks']={'eight_streams_finished':len(result['streams'])==8,'cancel_within_5s':result['cancel_release_seconds']<5,'request_first_page_p95_under_300ms':result['request_first_page_p95_ms']<300,'additional_ttft_p95_under_20ms':result['additional_ttft_p95_ms']<20,'rss_growth_under_64MiB':result['rss_growth_kib']<65536}
            output=Path(args.output);output.parent.mkdir(parents=True,exist_ok=True);output.write_text(json.dumps(result,indent=2)+'\n')
            print(json.dumps({'output':str(output),'build_id':result['build_id'],'checks':result['checks']},indent=2))
            if not all(result['checks'].values()): raise AssertionError('performance threshold not met; inspect actual evidence')
        finally:
            if process and process.poll() is None: process.terminate();process.wait(timeout=10)
            upstream.shutdown();upstream.server_close()

if __name__=='__main__':main()
