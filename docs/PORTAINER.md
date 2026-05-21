# Portainer

This document will outline how to add `gmmff` into a new stack in [Portainer](https://www.portainer.io/). These instructions were written on Portainer Business Edition v2.39.2.

## Guide

1. Log into Portainer
2. Select an environment
3. Select Stacks
4. Create a new stack (+ Add stack)
5. Give it a name e.g. gmmff
6. Paste in the contents of `configs/portainer.yml`
7. Under Environment variables, click on "Advanced mode"
8. Paste in the contents of `confis/stack.env`
9. Switch back to "Simple mode"
10. Edit each variable to match your enviroment
  - CONFIG is where your data will live. If that is in a folder called `/home/myuser/docker` then change it to there and add gmmff. For Synology: /volume1/docker/gmmff
  - Create the folder structure: `cd /home/myuser/docker && mkdir -p gmmff/{encrypted,redis}`
  - PUID/PGID should match your local use HINT: `id`
  - For help on any other env variable, please read `configs/.env.example` for what each variable does
11. Once you are ready to deploy, click on "Deploy the stack"

## Help

If this does not work for you, please open a new [Discussions](https://github.com/iamdoubz/gmmff/discussions) topic.