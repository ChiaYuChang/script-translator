{
  description = "Multi-language game script translation CLI tool (default: Japanese to Traditional Chinese zh-TW)";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs = { self, nixpkgs }:
    let
      version = "v0.4.1";
      sources = {
        x86_64-linux = {
          asset = "script-translator-linux-amd64";
          hash = "sha256-XkR7vVTtxz5w7Bd0HgpNpAgA58rrbB8gDqe+3oy12tI=";
        };
        aarch64-linux = {
          asset = "script-translator-linux-arm64";
          hash = "sha256-j8PCU1cla/4D8tkv6SE66ifDBtER7TzA5+txXNfLpyc=";
        };
        aarch64-darwin = {
          asset = "script-translator-darwin-arm64";
          hash = "sha256-J4LcKYG6BAUUCWJ0lbPzCCII8CPweBR2XSaoYbqqbGg=";
        };
      };

      supportedSystems = builtins.attrNames sources;
      forAllSystems = nixpkgs.lib.genAttrs supportedSystems;
    in
    {
      packages = forAllSystems (system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
          srcInfo = sources.${system};
          bin = pkgs.fetchurl {
            url = "https://github.com/ChiaYuChang/script-translator/releases/download/${version}/${srcInfo.asset}";
            hash = srcInfo.hash;
          };
          pkg = pkgs.stdenvNoCC.mkDerivation {
            pname = "script-translator";
            version = nixpkgs.lib.removePrefix "v" version;

            dontUnpack = true;
            dontConfigure = true;
            dontBuild = true;
            dontFixup = true;

            installPhase = ''
              runHook preInstall
              install -Dm755 ${bin} "$out/bin/script-translator"
              runHook postInstall
            '';

            meta = with pkgs.lib; {
              description = "Multi-language game script translation CLI tool using OpenAI-compatible Responses API (default: Japanese to Traditional Chinese zh-TW)";
              homepage = "https://github.com/ChiaYuChang/script-translator";
              license = licenses.mit;
              mainProgram = "script-translator";
              platforms = supportedSystems;
            };
          };
        in
        {
          default = pkg;
          script-translator = pkg;
        });

      apps = forAllSystems (system: {
        default = {
          type = "app";
          program = "${self.packages.${system}.default}/bin/script-translator";
        };
        script-translator = {
          type = "app";
          program = "${self.packages.${system}.script-translator}/bin/script-translator";
        };
      });

      overlays = rec {
        script-translator = final: _prev: {
          script-translator = self.packages.${final.stdenv.hostPlatform.system}.default;
        };
        default = script-translator;
      };

      homeManagerModules = rec {
        script-translator = import ./nix/hm-module.nix self;
        default = script-translator;
      };
    };
}
