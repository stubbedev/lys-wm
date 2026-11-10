scripts:
builtins.readFile ./vm-scripts.py
+ builtins.concatStringsSep "" (map (script: ''
    run_script("${script}")
  '')
  scripts)
