#!/bin/bash

function cleanup_project_automation() {
    # Get the remote URL and extract organization name
    local REMOTE_URL=$(git config --get remote.origin.url)
    local ORG_NAME=$(echo "$REMOTE_URL" | sed -E 's/.*[:/]([^/]*)\/.*/\1/')

    # Check if organization matches and delete file if it exists
    if [[ "$ORG_NAME" =~ ^(conduitio|conduitio-labs|meroxa)$ ]]; then
      echo "Keeping the project automation workflow for $ORG_NAME"
    else
      local FILE_PATH=".github/workflows/project_automation.yaml"
      if [ -f "$FILE_PATH" ]; then
        rm "$FILE_PATH"
          echo "Deleted $FILE_PATH"
      else
        echo "File $FILE_PATH not found"
      fi
    fi
}

if [ $# -eq 0 ]
then
    echo "Module name not provided."
    exit 1
fi

MODULE_NAME=$1
if ! [[ "$MODULE_NAME" =~ ^github.com\/.*\/conduit-connector-(.*)$ ]]
then
  echo "Module name ${MODULE_NAME} not in recommended format \"github.com/repository/conduit-connector-connectorname\"."
  echo
  echo "Certain things (such as pull request templates) will not work correctly."
  while true; do
      read -n1 -p "Are you sure you want to continue? [y/n] " yn
      echo
      case $yn in
          [Yy]* ) break;;
          [Nn]* ) exit;;
          * ) echo "Please answer yes or no.";;
      esac
  done
fi

CONNECTOR_NAME=${BASH_REMATCH[1]}

if [[ "$OSTYPE" == "darwin"* ]]; then
  LC_ALL=C find . -type f ! -name "setup.sh" -exec sed -i "" "s~github.com/conduitio/conduit-connector-connectorname~$MODULE_NAME~g" {} +
  LC_ALL=C find . -type f ! -name "setup.sh" -exec sed -i "" "s~connectorname~$CONNECTOR_NAME~g" {} +
  LC_ALL=C sed -i "" "s~*       @ConduitIO/conduit-core~ ~g" .github/CODEOWNERS
else
  find . -type f ! -name "setup.sh" -exec sed -i "s~github.com/conduitio/conduit-connector-connectorname~$MODULE_NAME~g" {} +
  find . -type f ! -name "setup.sh" -exec sed -i "s~connectorname~$CONNECTOR_NAME~g" {} +
  sed -i "s~*       @ConduitIO/conduit-core~ ~g" .github/CODEOWNERS
fi

cleanup_project_automation

# Remove this script
rm "$0"
rm README.md
mv README_TEMPLATE.md README.md

if ! command -v conn-sdk-cli >/dev/null 2>&1; then
    echo "conn-sdk-cli is not installed, installing tools needed for connector development"
    make install-tools
fi
make generate
