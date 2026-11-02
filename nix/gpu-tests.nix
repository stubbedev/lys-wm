{
  pkgs,
  lys-wm,
  lys-ui,
}: let
  packages = ["render/vulkan" "render/gles" "harness"];
  tests = lys-wm.overrideAttrs (old: {
    pname = "lys-wm-gpu-tests";
    doCheck = false;
    buildPhase = ''
      runHook preBuild
      mkdir -p "$out/bin"
      ${pkgs.lib.concatMapStringsSep "\n" (p: ''go test -c -o "$out/bin/${baseNameOf p}.test" ./internal/${p}'') packages}
      runHook postBuild
    '';
    installPhase = ''
      runHook preInstall
      cp -r internal "$out/internal"
      mkdir -p "$out/docs"
      cp -r docs/shaders "$out/docs/shaders"
      runHook postInstall
    '';
    postFixup = "";
  });
  env = import ./test-env.nix pkgs;
  run = {
    name,
    dir,
    tests,
    allowSkips,
  }: ''
    machine.succeed(
        "cd ${tests_}/internal/${dir} && LYS_WM_HEADLESS=1 HOME=/tmp TMPDIR=/tmp timeout 280 ${tests_}/bin/${name}.test "
        "-test.count=1 -test.v -test.run '${tests}' > /tmp/${name}.log 2>&1 || (cat /tmp/${name}.log; exit 1)",
        timeout=300,
    )
    print(machine.succeed("grep -E '^(--- |    --- |ok|FAIL|PASS|.*_test.go:.*(frame|nested|async))' /tmp/${name}.log"))
    assert_ran("/tmp/${name}.log", "${allowSkips}")
  '';
  tests_ = tests;
in
  pkgs.testers.runNixOSTest {
    name = "lys-wm-gpu-tests";
    nodes.machine = {
      boot.kernelModules = ["udmabuf"];
      virtualisation = {
        cores = 4;
        memorySize = 4096;
      };
      environment = {
        variables = env // {GBM_BACKENDS_PATH = "${pkgs.mesa}/lib/gbm";};
        systemPackages = import ./test-programs.nix {inherit pkgs lys-ui;};
      };
    };
    testScript = ''
      import re

      def assert_ran(log, allowed):
          skipped = re.findall(r"--- SKIP: (\S+)", machine.succeed(f"cat {log}"))
          unexpected = [s for s in skipped if not re.search(allowed, s)]
          assert not unexpected, f"these tests did not run: {unexpected}"

      machine.wait_for_unit("multi-user.target")
      machine.succeed("test -c /dev/udmabuf")
      ${run {
        name = "vulkan";
        dir = "render/vulkan";
        tests = ".";
        allowSkips = "OnHardware";
      }}
      ${run {
        name = "gles";
        dir = "render/gles";
        tests = "AsyncRead";
        allowSkips = "^$";
      }}
      ${run {
        name = "harness";
        dir = "harness";
        tests = "TestABlurRegion|TestPopupsBlurByTheirRegion|TestAPopupsFirstFrame|TestDecorationBlurReachesPopups|Nested(LysWMRendersWithVulkan|GLESNames|GPURenderers)|FrameCost|OnTheGPURenderers|TestADMABUFDestroyed|TestFullscreenDMABUFs|TestAFullscreenDMABUF|TestAnARGBDMABUF|TestATenBit|TestDestroyingAReplaced|TestAFullscreenSurface|TestATranslucentFullscreen|TestARoundedWindow|TestImageCopyCaptureIntoDMABUFs|TestACaptureIntoADMABUF|TestAFrameDestroyedWhile|TestScaledClientCursors|TestTenBitShm|TheVulkanRenderer|TestIntegerScale|TestAScreenShader|TestTheExampleShaders|TestTheLockScreenAndScreencopy|TestAGenie|TestScanoutStopsOnly";
        allowSkips = "OnHardware|TestScreencopyIntoDMABUFs|TestACaptureIntoADMABUF|TestNestedFrameCost/gles|TestAnARGBDMABUF|TestATenBitDMABUF";
      }}
    '';
  }
