self: {
  config,
  lib,
  pkgs,
  ...
}: let
  cfg = config.programs.script-translator;
  jsonFormat = pkgs.formats.json {};
  configFile = jsonFormat.generate "script-translator-config.json" cfg.settings;
in {
  options.programs.script-translator = {
    enable = lib.mkEnableOption "script-translator CLI tool";

    package = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
      defaultText = lib.literalExpression "inputs.script-translator.packages.\${pkgs.stdenv.hostPlatform.system}.default";
      description = "The script-translator package to install.";
    };

    settings = lib.mkOption {
      type = jsonFormat.type;
      default = {};
      example = lib.literalExpression ''
        {
          language = {
            from = "Japanese";
            to = "Traditional Chinese";
            location = "Taiwan (zh-TW)";
          };
          provider = {
            base_url = "https://api.openai.com/v1";
            api_key_env = "OPENAI_API_KEY";
            wait_time = 1;
            retry_time = 5;
          };
          entity_recognition = {
            model = "gpt-4.1-mini";
          };
          translation = {
            model = "gpt-4.1";
            batch_size = 20;
          };
        }
      '';
      description = ''
        Configuration written declaratively to `script-translator/config.json`
        when non-empty. Supports top-level `language` (`from`, `to`, optional
        `location`), `provider`, `entity_recognition` (including optional
        few-shot `example`), `translation` (including optional few-shot
        `example`), and `observation`. Prefer `provider.api_key_env` over inline
        `provider.api_key` since Nix store files are world-readable.
      '';
    };
  };

  config = lib.mkIf cfg.enable (lib.mkMerge [
    {
      home.packages = [cfg.package];
    }
    (lib.mkIf (cfg.settings != {} && !pkgs.stdenv.hostPlatform.isDarwin) {
      xdg.configFile."script-translator/config.json".source = configFile;
    })
    (lib.mkIf (cfg.settings != {} && pkgs.stdenv.hostPlatform.isDarwin) {
      home.file."Library/Application Support/script-translator/config.json".source = configFile;
    })
  ]);
}
