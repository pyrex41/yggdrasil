#!/usr/bin/env python3
import hashlib,json,os,pathlib,re,subprocess,time
root=pathlib.Path.cwd();out=root/'out-shen-c-rc-evidence';runtime=root.parent/'shen-c-release-0.3.0'
sha=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
env=dict(os.environ,PATH='<HOME>/.nix-profile/bin:'+os.environ['PATH'],SHEN_C=str(runtime),CC='/usr/bin/cc',CFLAGS='-O3',CPPFLAGS='')
for key in ['SHEN_RC_PRINT','SHEN_RC_STATS']:env.pop(key,None)
record={'scope':'Native RC adapter qualification against existing pristine 42.2 shaken inputs and full-kernel control; not a performance or cross-platform claim','runtime_revision':subprocess.check_output(['git','rev-parse','HEAD'],cwd=runtime,text=True).strip(),'runtime_sources_sha256':{p.name:sha(p) for p in (runtime/'src/rc').glob('*.[ch]')},'builder_sha256':sha(runtime/'bin/rc-build'),'adapter_sources_sha256':{str(p.relative_to(root)):sha(p) for p in [root/'builders/c/build.sh',root/'builders/c/compile.py',root/'builders.json',root/'c_builder_test.go']},'compiler':subprocess.check_output(['/usr/bin/cc','--version'],text=True),'flags':'-O3 -std=c17; suffix views off','runs':[]}
for label,source,expected in [('hello','out-shen-c-rc-hello','hello from shaken shen\n'),('fib','out-shen-c-rc-fib','fib 20 = 6765\n'),('tc-full','out-shen-c-rc-tc-full',None)]:
 source=root/source;manifest=source/'yggdrasil.manifest.txt';inputs={str(manifest.relative_to(root)):sha(manifest)}
 for line in manifest.read_text().splitlines():
  key,sep,value=line.partition('=')
  if sep and key in ['kernel','user']:inputs[str((source/value).relative_to(root))]=sha(source/value)
 target=source/'app-c-final';command=['bash',str(root/'builders/c/build.sh'),str(source),str(target)];start=time.monotonic()
 with (out/(label+'-final-build.log')).open('wb') as log:p=subprocess.run(command,env=env,stdout=log,stderr=subprocess.STDOUT,timeout=240)
 item={'case':label,'inputs_sha256':inputs,'command':command,'build_exit':p.returncode,'build_seconds':time.monotonic()-start};record['runs'].append(item)
 assert p.returncode==0,item
 run=subprocess.run([str(target/'app')],cwd=root/'tests' if label=='tc-full' else root,env=env,capture_output=True,timeout=90)
 (out/(label+'-final.out')).write_bytes(run.stdout);(out/(label+'-final.err')).write_bytes(run.stderr)
 assert run.returncode==0 and not run.stderr,(label,run.returncode,run.stderr)
 if expected is not None:assert run.stdout.decode()==expected,(label,run.stdout)
 else:assert re.search(r'^normal-form = 7$',run.stdout.decode(),re.M) and 'typechecked in ' in run.stdout.decode() and 'undefined function' not in run.stdout.decode(),run.stdout
 audit=subprocess.run([str(runtime/'scripts/test-rc.sh'),'--audit-only',str(target/'app')],env=env,capture_output=True,timeout=20)
 (out/(label+'-final-audit.log')).write_bytes(audit.stdout+audit.stderr);assert audit.returncode==0
 assert all(sha(root/p)==h for p,h in inputs.items())
 item.update(status='PASS',run_exit=run.returncode,stdout_sha256=hashlib.sha256(run.stdout).hexdigest(),binary_sha256=sha(target/'app'),generated_c_sha256=sha(target/'app.c'),inputs_unchanged=True,audit='PASS: no Boehm symbols/dependency')
 (out/'final.json').write_text(json.dumps(record,indent=2)+'\n');print(label,'PASS',flush=True)
assert (out/'metadata-final.out').read_text()=='arity = 1\n'
record['metadata']='PASS: packaged CLI reports declared user arity 1; pre-adapter attempts returned -1'
record['dynamic_load_slice']='OPEN: shaken tc-interp omits fix used by runtime-loaded interpreter; unshaken control passes normal-form = 7'
record['status']='PASS for stated adapter controls; dynamic-load slice OPEN'
(out/'final.json').write_text(json.dumps(record,indent=2)+'\n')
