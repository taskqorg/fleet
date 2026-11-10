# The executor binary for these scripts; same rules as tests/executor/executor-binary.ts: build first (a shared target
# dir may hold another worktree's build), find the target dir through cargo metadata, run a private copy.
import json,os,shutil,subprocess,tempfile
def executor_binary():
    subprocess.run(['cargo','build','-p','fleet-executor'],check=True)
    target=json.loads(subprocess.check_output(['cargo','metadata','--format-version','1','--no-deps']))['target_directory']
    copy=os.path.join(tempfile.mkdtemp(prefix='fleet-executor-'),'fleet-executor')
    shutil.copy2(os.path.join(target,'debug','fleet-executor'),copy)
    return copy


def secret_file(value):
    """A private host secret file for FLEET_HOST_SECRET_FILE (people-access §3)."""
    import tempfile, pathlib
    p = pathlib.Path(tempfile.mkdtemp()) / 'host-secret'
    p.write_text(value + '\n'); p.chmod(0o600)
    return str(p)
