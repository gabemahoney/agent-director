import os, subprocess, sys, pathlib, datetime
root=pathlib.Path.cwd()
d=root/'tickets/handoff/b.ftd/Plans/b.jf1/e3-task3/commit-closeout-cancel-barrier-full'
env=dict(os.environ)
remove=[k for k in env if (k.startswith('AGENT_DIRECTOR_') and k!='AGENT_DIRECTOR_TEST_SANDBOX') or k in ('TMUX','TMUX_PANE','GIT_CONFIG_GLOBAL','TMPDIR','MAKEFLAGS','MFLAGS','CMD','GOFLAGS','SANDBOX_FLAGS')]
for k in remove: env.pop(k,None)
env['SANDBOX_FLAGS']='--env-file '+str(d/'container.env')
os.umask(0o022)
mode=sys.argv[1]
if mode=='full':
    (d/'removed-environment-names.txt').write_text('\n'.join(sorted(remove))+'\n')
    (d/'start.txt').write_text(datetime.datetime.now(datetime.timezone.utc).isoformat()+'\n')
    with open(d/'full.log','w',opener=lambda p,f:os.open(p,f,0o600)) as log:
        result=subprocess.run(['make','test-sandbox'],env=env,stdout=log,stderr=subprocess.STDOUT)
    (d/'make-exit.txt').write_text(str(result.returncode)+'\n')
    (d/'end.txt').write_text(datetime.datetime.now(datetime.timezone.utc).isoformat()+'\n')
    print('make exit:',result.returncode)
    sys.exit(result.returncode)
else:
    args=['make','sandbox','CMD='+sys.argv[2]]
    if mode=='targeted':
        with open(d/'targeted.log','w',opener=lambda p,f:os.open(p,f,0o600)) as log:
            result=subprocess.run(args,env=env,stdout=log,stderr=subprocess.STDOUT)
        (d/'targeted-exit.txt').write_text(str(result.returncode)+'\n')
        print('targeted exit:',result.returncode)
        sys.exit(result.returncode)
    else: sys.exit(subprocess.run(args,env=env).returncode)
