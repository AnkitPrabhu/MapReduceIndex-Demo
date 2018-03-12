#include "Wrapper.h"
#include "Client.hpp"
#include "Messages.h"
#include<unistd.h>
#include<time.h>

EngineObj CreateEngine(int NumberOfIsolates){
    if(!e){
        Engine *trying=new Engine(NumberOfIsolates);
        e=(void*)trying;
    }
    return (void*)e;
}

void Compile(char* filename,EngineObj e){
    Engine *e1=(Engine*)e;
    e1->Compile(std::string(filename));
}

returnType Route(EngineObj e,struct metaData meta,const char* doc,const char* filename){
    Engine *e1=(Engine*)e;
    auto ans = e1->Route(meta, doc,filename);
    return ans;
}

int getLength(void* msg){
    msg_response* m=(msg_response*)msg;
    return m->length;
}

int getType(returnType msg,int index){
    msg_response* m=(msg_response*)msg;
    return m->type[index];
}

void* GetValue(returnType msg){
    msg_response* m=(msg_response*)msg;
    return (void*)&m->arr;
}

void* GetTypeArray(returnType msg){
    msg_response* m=(msg_response*)msg;
    return (void*)&m->type;
}
const char* getJSON(returnType msg,int index){
    msg_response* m=(msg_response*)msg;
    return m->arr[index].stringValue.c_str();
}

const char* getString(returnType msg,int index){
    msg_response* m=(msg_response*)msg;
    return m->arr[index].stringValue.c_str();
}

int64_t getInt(returnType msg,int index){
    msg_response* m=(msg_response*)msg;
    return m->arr[index].intValue;
}

double getFloat(returnType msg,int index){
    msg_response* m=(msg_response*)msg;
    return m->arr[index].doubleValue;
}

int getBool(returnType msg,int index){
    msg_response* m=(msg_response*)msg;
    return m->arr[index].boolValue;
}

